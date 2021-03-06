package commands

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"time"

	key "github.com/ipfs/go-ipfs/blocks/key"
	cmds "github.com/ipfs/go-ipfs/commands"
	dag "github.com/ipfs/go-ipfs/merkledag"
	notif "github.com/ipfs/go-ipfs/notifications"
	path "github.com/ipfs/go-ipfs/path"
	routing "github.com/ipfs/go-ipfs/routing"
	ipdht "github.com/ipfs/go-ipfs/routing/dht"
	pstore "gx/ipfs/QmQdnfvZQuhdT93LNc5bos52wAmdr3G2p6G8teLJMEN32P/go-libp2p-peerstore"
	peer "gx/ipfs/QmRBqJF7hb8ZSpRcMwUt8hNhydWcxGEhtk81HKq6oUwKvs/go-libp2p-peer"
	u "gx/ipfs/QmZNVWh8LLjAavuQ2JXuFmuYH3C11xo988vSgp7UQrTRj1/go-ipfs-util"
	"gx/ipfs/QmZy2y8t9zQH2a1b8q2ZSLKp17ATuJoCNxxyMFG5qFExpt/go-net/context"
)

var ErrNotDHT = errors.New("routing service is not a DHT")

var DhtCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline:          "Issue commands directly through the DHT.",
		ShortDescription: ``,
	},

	Subcommands: map[string]*cmds.Command{
		"query":     queryDhtCmd,
		"findprovs": findProvidersDhtCmd,
		"findpeer":  findPeerDhtCmd,
		"get":       getValueDhtCmd,
		"put":       putValueDhtCmd,
		"provide":   provideRefDhtCmd,
	},
}

var queryDhtCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline:          "Find the closest Peer IDs to a given Peer ID by querying the DHT.",
		ShortDescription: "Outputs a list of newline-delimited Peer IDs.",
	},

	Arguments: []cmds.Argument{
		cmds.StringArg("peerID", true, true, "The peerID to run the query against."),
	},
	Options: []cmds.Option{
		cmds.BoolOption("verbose", "v", "Print extra information.").Default(false),
	},
	Run: func(req cmds.Request, res cmds.Response) {
		n, err := req.InvocContext().GetNode()
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		dht, ok := n.Routing.(*ipdht.IpfsDHT)
		if !ok {
			res.SetError(ErrNotDHT, cmds.ErrNormal)
			return
		}

		events := make(chan *notif.QueryEvent)
		ctx := notif.RegisterForQueryEvents(req.Context(), events)

		closestPeers, err := dht.GetClosestPeers(ctx, key.Key(req.Arguments()[0]))
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		go func() {
			defer close(events)
			for p := range closestPeers {
				notif.PublishQueryEvent(ctx, &notif.QueryEvent{
					ID:   p,
					Type: notif.FinalPeer,
				})
			}
		}()

		outChan := make(chan interface{})
		res.SetOutput((<-chan interface{})(outChan))

		go func() {
			defer close(outChan)
			for e := range events {
				outChan <- e
			}
		}()
	},
	Marshalers: cmds.MarshalerMap{
		cmds.Text: func(res cmds.Response) (io.Reader, error) {
			outChan, ok := res.Output().(<-chan interface{})
			if !ok {
				return nil, u.ErrCast()
			}

			pfm := pfuncMap{
				notif.PeerResponse: func(obj *notif.QueryEvent, out io.Writer, verbose bool) {
					for _, p := range obj.Responses {
						fmt.Fprintf(out, "%s\n", p.ID.Pretty())
					}
				},
			}

			marshal := func(v interface{}) (io.Reader, error) {
				obj, ok := v.(*notif.QueryEvent)
				if !ok {
					return nil, u.ErrCast()
				}

				verbose, _, _ := res.Request().Option("v").Bool()

				buf := new(bytes.Buffer)
				printEvent(obj, buf, verbose, pfm)
				return buf, nil
			}

			return &cmds.ChannelMarshaler{
				Channel:   outChan,
				Marshaler: marshal,
				Res:       res,
			}, nil
		},
	},
	Type: notif.QueryEvent{},
}

var findProvidersDhtCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline:          "Find peers in the DHT that can provide a specific value, given a key.",
		ShortDescription: "Outputs a list of newline-delimited provider Peer IDs.",
	},

	Arguments: []cmds.Argument{
		cmds.StringArg("key", true, true, "The key to find providers for."),
	},
	Options: []cmds.Option{
		cmds.BoolOption("verbose", "v", "Print extra information.").Default(false),
	},
	Run: func(req cmds.Request, res cmds.Response) {
		n, err := req.InvocContext().GetNode()
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		dht, ok := n.Routing.(*ipdht.IpfsDHT)
		if !ok {
			res.SetError(ErrNotDHT, cmds.ErrNormal)
			return
		}

		numProviders := 20

		outChan := make(chan interface{})
		res.SetOutput((<-chan interface{})(outChan))

		events := make(chan *notif.QueryEvent)
		ctx := notif.RegisterForQueryEvents(req.Context(), events)

		pchan := dht.FindProvidersAsync(ctx, key.B58KeyDecode(req.Arguments()[0]), numProviders)
		go func() {
			defer close(outChan)
			for e := range events {
				outChan <- e
			}
		}()

		go func() {
			defer close(events)
			for p := range pchan {
				np := p
				notif.PublishQueryEvent(ctx, &notif.QueryEvent{
					Type:      notif.Provider,
					Responses: []*pstore.PeerInfo{&np},
				})
			}
		}()
	},
	Marshalers: cmds.MarshalerMap{
		cmds.Text: func(res cmds.Response) (io.Reader, error) {
			outChan, ok := res.Output().(<-chan interface{})
			if !ok {
				return nil, u.ErrCast()
			}

			verbose, _, _ := res.Request().Option("v").Bool()
			pfm := pfuncMap{
				notif.FinalPeer: func(obj *notif.QueryEvent, out io.Writer, verbose bool) {
					if verbose {
						fmt.Fprintf(out, "* closest peer %s\n", obj.ID)
					}
				},
				notif.Provider: func(obj *notif.QueryEvent, out io.Writer, verbose bool) {
					prov := obj.Responses[0]
					if verbose {
						fmt.Fprintf(out, "provider: ")
					}
					fmt.Fprintf(out, "%s\n", prov.ID.Pretty())
					if verbose {
						for _, a := range prov.Addrs {
							fmt.Fprintf(out, "\t%s\n", a)
						}
					}
				},
			}

			marshal := func(v interface{}) (io.Reader, error) {
				obj, ok := v.(*notif.QueryEvent)
				if !ok {
					return nil, u.ErrCast()
				}

				buf := new(bytes.Buffer)
				printEvent(obj, buf, verbose, pfm)
				return buf, nil
			}

			return &cmds.ChannelMarshaler{
				Channel:   outChan,
				Marshaler: marshal,
				Res:       res,
			}, nil
		},
	},
	Type: notif.QueryEvent{},
}

var provideRefDhtCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline: "Announce to the network that you are providing given values.",
	},

	Arguments: []cmds.Argument{
		cmds.StringArg("key", true, true, "The key[s] to send provide records for.").EnableStdin(),
	},
	Options: []cmds.Option{
		cmds.BoolOption("verbose", "v", "Print extra information.").Default(false),
		cmds.BoolOption("recursive", "r", "Recursively provide entire graph.").Default(false),
	},
	Run: func(req cmds.Request, res cmds.Response) {
		n, err := req.InvocContext().GetNode()
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		if n.Routing == nil {
			res.SetError(errNotOnline, cmds.ErrNormal)
			return
		}

		rec, _, _ := req.Option("recursive").Bool()

		var keys []key.Key
		for _, arg := range req.Arguments() {
			k := key.B58KeyDecode(arg)
			if k == "" {
				res.SetError(fmt.Errorf("incorrectly formatted key: ", arg), cmds.ErrNormal)
				return
			}

			has, err := n.Blockstore.Has(k)
			if err != nil {
				res.SetError(err, cmds.ErrNormal)
				return
			}

			if !has {
				res.SetError(fmt.Errorf("block %s not found locally, cannot provide", k), cmds.ErrNormal)
				return
			}

			keys = append(keys, k)
		}

		outChan := make(chan interface{})
		res.SetOutput((<-chan interface{})(outChan))

		events := make(chan *notif.QueryEvent)
		ctx := notif.RegisterForQueryEvents(req.Context(), events)

		go func() {
			defer close(outChan)
			for e := range events {
				outChan <- e
			}
		}()

		go func() {
			defer close(events)
			var err error
			if rec {
				err = provideKeysRec(ctx, n.Routing, n.DAG, keys)
			} else {
				err = provideKeys(ctx, n.Routing, keys)
			}
			if err != nil {
				notif.PublishQueryEvent(ctx, &notif.QueryEvent{
					Type:  notif.QueryError,
					Extra: err.Error(),
				})
			}
		}()
	},
	Marshalers: cmds.MarshalerMap{
		cmds.Text: func(res cmds.Response) (io.Reader, error) {
			outChan, ok := res.Output().(<-chan interface{})
			if !ok {
				return nil, u.ErrCast()
			}

			verbose, _, _ := res.Request().Option("v").Bool()
			pfm := pfuncMap{
				notif.FinalPeer: func(obj *notif.QueryEvent, out io.Writer, verbose bool) {
					if verbose {
						fmt.Fprintf(out, "sending provider record to peer %s\n", obj.ID)
					}
				},
			}

			marshal := func(v interface{}) (io.Reader, error) {
				obj, ok := v.(*notif.QueryEvent)
				if !ok {
					return nil, u.ErrCast()
				}

				buf := new(bytes.Buffer)
				printEvent(obj, buf, verbose, pfm)
				return buf, nil
			}

			return &cmds.ChannelMarshaler{
				Channel:   outChan,
				Marshaler: marshal,
				Res:       res,
			}, nil
		},
	},
	Type: notif.QueryEvent{},
}

func provideKeys(ctx context.Context, r routing.IpfsRouting, keys []key.Key) error {
	for _, k := range keys {
		err := r.Provide(ctx, k)
		if err != nil {
			return err
		}
	}
	return nil
}

func provideKeysRec(ctx context.Context, r routing.IpfsRouting, dserv dag.DAGService, keys []key.Key) error {
	provided := make(map[key.Key]struct{})
	for _, k := range keys {
		kset := key.NewKeySet()
		node, err := dserv.Get(ctx, k)
		if err != nil {
			return err
		}

		err = dag.EnumerateChildrenAsync(ctx, dserv, node, kset)
		if err != nil {
			return err
		}

		for _, k := range kset.Keys() {
			if _, ok := provided[k]; ok {
				continue
			}

			err = r.Provide(ctx, k)
			if err != nil {
				return err
			}
			provided[k] = struct{}{}
		}
	}

	return nil
}

var findPeerDhtCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline:          "Query the DHT for all of the multiaddresses associated with a Peer ID.",
		ShortDescription: "Outputs a list of newline-delimited multiaddresses.",
	},

	Arguments: []cmds.Argument{
		cmds.StringArg("peerID", true, true, "The ID of the peer to search for."),
	},
	Options: []cmds.Option{
		cmds.BoolOption("verbose", "v", "Print extra information.").Default(false),
	},
	Run: func(req cmds.Request, res cmds.Response) {
		n, err := req.InvocContext().GetNode()
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		dht, ok := n.Routing.(*ipdht.IpfsDHT)
		if !ok {
			res.SetError(ErrNotDHT, cmds.ErrNormal)
			return
		}

		pid, err := peer.IDB58Decode(req.Arguments()[0])
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		outChan := make(chan interface{})
		res.SetOutput((<-chan interface{})(outChan))

		events := make(chan *notif.QueryEvent)
		ctx := notif.RegisterForQueryEvents(req.Context(), events)

		go func() {
			defer close(outChan)
			for v := range events {
				outChan <- v
			}
		}()

		go func() {
			defer close(events)
			pi, err := dht.FindPeer(ctx, pid)
			if err != nil {
				notif.PublishQueryEvent(ctx, &notif.QueryEvent{
					Type:  notif.QueryError,
					Extra: err.Error(),
				})
				return
			}

			notif.PublishQueryEvent(ctx, &notif.QueryEvent{
				Type:      notif.FinalPeer,
				Responses: []*pstore.PeerInfo{&pi},
			})
		}()
	},
	Marshalers: cmds.MarshalerMap{
		cmds.Text: func(res cmds.Response) (io.Reader, error) {
			outChan, ok := res.Output().(<-chan interface{})
			if !ok {
				return nil, u.ErrCast()
			}

			verbose, _, _ := res.Request().Option("v").Bool()

			pfm := pfuncMap{
				notif.FinalPeer: func(obj *notif.QueryEvent, out io.Writer, verbose bool) {
					pi := obj.Responses[0]
					for _, a := range pi.Addrs {
						fmt.Fprintf(out, "%s\n", a)
					}
				},
			}
			marshal := func(v interface{}) (io.Reader, error) {
				obj, ok := v.(*notif.QueryEvent)
				if !ok {
					return nil, u.ErrCast()
				}

				buf := new(bytes.Buffer)
				printEvent(obj, buf, verbose, pfm)
				return buf, nil
			}

			return &cmds.ChannelMarshaler{
				Channel:   outChan,
				Marshaler: marshal,
				Res:       res,
			}, nil
		},
	},
	Type: notif.QueryEvent{},
}

var getValueDhtCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline: "Given a key, query the DHT for its best value.",
		ShortDescription: `
Outputs the best value for the given key.

There may be several different values for a given key stored in the DHT; in
this context 'best' means the record that is most desirable. There is no one
metric for 'best': it depends entirely on the key type. For IPNS, 'best' is
the record that is both valid and has the highest sequence number (freshest).
Different key types can specify other 'best' rules.
`,
	},

	Arguments: []cmds.Argument{
		cmds.StringArg("key", true, true, "The key to find a value for."),
	},
	Options: []cmds.Option{
		cmds.BoolOption("verbose", "v", "Print extra information.").Default(false),
	},
	Run: func(req cmds.Request, res cmds.Response) {
		n, err := req.InvocContext().GetNode()
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		dht, ok := n.Routing.(*ipdht.IpfsDHT)
		if !ok {
			res.SetError(ErrNotDHT, cmds.ErrNormal)
			return
		}

		outChan := make(chan interface{})
		res.SetOutput((<-chan interface{})(outChan))

		events := make(chan *notif.QueryEvent)
		ctx := notif.RegisterForQueryEvents(req.Context(), events)

		dhtkey, err := escapeDhtKey(req.Arguments()[0])
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		go func() {
			defer close(outChan)
			for e := range events {
				outChan <- e
			}
		}()

		go func() {
			defer close(events)
			val, err := dht.GetValue(ctx, dhtkey)
			if err != nil {
				notif.PublishQueryEvent(ctx, &notif.QueryEvent{
					Type:  notif.QueryError,
					Extra: err.Error(),
				})
			} else {
				notif.PublishQueryEvent(ctx, &notif.QueryEvent{
					Type:  notif.Value,
					Extra: string(val),
				})
			}
		}()
	},
	Marshalers: cmds.MarshalerMap{
		cmds.Text: func(res cmds.Response) (io.Reader, error) {
			outChan, ok := res.Output().(<-chan interface{})
			if !ok {
				return nil, u.ErrCast()
			}

			verbose, _, _ := res.Request().Option("v").Bool()

			pfm := pfuncMap{
				notif.Value: func(obj *notif.QueryEvent, out io.Writer, verbose bool) {
					if verbose {
						fmt.Fprintf(out, "got value: '%s'\n", obj.Extra)
					} else {
						fmt.Fprintln(out, obj.Extra)
					}
				},
			}
			marshal := func(v interface{}) (io.Reader, error) {
				obj, ok := v.(*notif.QueryEvent)
				if !ok {
					return nil, u.ErrCast()
				}

				buf := new(bytes.Buffer)

				printEvent(obj, buf, verbose, pfm)

				return buf, nil
			}

			return &cmds.ChannelMarshaler{
				Channel:   outChan,
				Marshaler: marshal,
				Res:       res,
			}, nil
		},
	},
	Type: notif.QueryEvent{},
}

var putValueDhtCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline: "Write a key/value pair to the DHT.",
		ShortDescription: `
Given a key of the form /foo/bar and a value of any form, this will write that
value to the DHT with that key.

Keys have two parts: a keytype (foo) and the key name (bar). IPNS uses the
/ipns keytype, and expects the key name to be a Peer ID. IPNS entries are
specifically formatted (protocol buffer).

You may only use keytypes that are supported in your ipfs binary: currently
this is only /ipns. Unless you have a relatively deep understanding of the
go-ipfs DHT internals, you likely want to be using 'ipfs name publish' instead
of this.

Value is arbitrary text. Standard input can be used to provide value.

NOTE: A value may not exceed 2048 bytes.
`,
	},

	Arguments: []cmds.Argument{
		cmds.StringArg("key", true, false, "The key to store the value at."),
		cmds.StringArg("value", true, false, "The value to store.").EnableStdin(),
	},
	Options: []cmds.Option{
		cmds.BoolOption("verbose", "v", "Print extra information.").Default(false),
	},
	Run: func(req cmds.Request, res cmds.Response) {
		n, err := req.InvocContext().GetNode()
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		dht, ok := n.Routing.(*ipdht.IpfsDHT)
		if !ok {
			res.SetError(ErrNotDHT, cmds.ErrNormal)
			return
		}

		outChan := make(chan interface{})
		res.SetOutput((<-chan interface{})(outChan))

		events := make(chan *notif.QueryEvent)
		ctx := notif.RegisterForQueryEvents(req.Context(), events)

		key, err := escapeDhtKey(req.Arguments()[0])
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		data := req.Arguments()[1]

		go func() {
			defer close(outChan)
			for e := range events {
				outChan <- e
			}
		}()

		go func() {
			defer close(events)
			err := dht.PutValue(ctx, key, []byte(data))
			if err != nil {
				notif.PublishQueryEvent(ctx, &notif.QueryEvent{
					Type:  notif.QueryError,
					Extra: err.Error(),
				})
			}
		}()
	},
	Marshalers: cmds.MarshalerMap{
		cmds.Text: func(res cmds.Response) (io.Reader, error) {
			outChan, ok := res.Output().(<-chan interface{})
			if !ok {
				return nil, u.ErrCast()
			}

			verbose, _, _ := res.Request().Option("v").Bool()
			pfm := pfuncMap{
				notif.FinalPeer: func(obj *notif.QueryEvent, out io.Writer, verbose bool) {
					if verbose {
						fmt.Fprintf(out, "* closest peer %s\n", obj.ID)
					}
				},
				notif.Value: func(obj *notif.QueryEvent, out io.Writer, verbose bool) {
					fmt.Fprintf(out, "%s\n", obj.ID.Pretty())
				},
			}

			marshal := func(v interface{}) (io.Reader, error) {
				obj, ok := v.(*notif.QueryEvent)
				if !ok {
					return nil, u.ErrCast()
				}

				buf := new(bytes.Buffer)
				printEvent(obj, buf, verbose, pfm)

				return buf, nil
			}

			return &cmds.ChannelMarshaler{
				Channel:   outChan,
				Marshaler: marshal,
				Res:       res,
			}, nil
		},
	},
	Type: notif.QueryEvent{},
}

type printFunc func(obj *notif.QueryEvent, out io.Writer, verbose bool)
type pfuncMap map[notif.QueryEventType]printFunc

func printEvent(obj *notif.QueryEvent, out io.Writer, verbose bool, override pfuncMap) {
	if verbose {
		fmt.Fprintf(out, "%s: ", time.Now().Format("15:04:05.000"))
	}

	if override != nil {
		if pf, ok := override[obj.Type]; ok {
			pf(obj, out, verbose)
			return
		}
	}

	switch obj.Type {
	case notif.SendingQuery:
		if verbose {
			fmt.Fprintf(out, "* querying %s\n", obj.ID)
		}
	case notif.Value:
		if verbose {
			fmt.Fprintf(out, "got value: '%s'\n", obj.Extra)
		} else {
			fmt.Fprint(out, obj.Extra)
		}
	case notif.PeerResponse:
		if verbose {
			fmt.Fprintf(out, "* %s says use ", obj.ID)
			for _, p := range obj.Responses {
				fmt.Fprintf(out, "%s ", p.ID)
			}
			fmt.Fprintln(out)
		}
	case notif.QueryError:
		if verbose {
			fmt.Fprintf(out, "error: %s\n", obj.Extra)
		}
	case notif.DialingPeer:
		if verbose {
			fmt.Fprintf(out, "dialing peer: %s\n", obj.ID)
		}
	case notif.AddingPeer:
		if verbose {
			fmt.Fprintf(out, "adding peer to query: %s\n", obj.ID)
		}
	case notif.FinalPeer:
	default:
		if verbose {
			fmt.Fprintf(out, "unrecognized event type: %d\n", obj.Type)
		}
	}
}

func escapeDhtKey(s string) (key.Key, error) {
	parts := path.SplitList(s)
	switch len(parts) {
	case 1:
		return key.B58KeyDecode(s), nil
	case 3:
		k := key.B58KeyDecode(parts[2])
		return key.Key(path.Join(append(parts[:2], string(k)))), nil
	default:
		return "", errors.New("invalid key")
	}
}
