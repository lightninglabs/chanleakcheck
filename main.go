package main

import (
	"context"
	"flag"
	"log"
	"math"
	"path/filepath"
	"time"

	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnwire"
)

var (
	defaultLndDir          = btcutil.AppDataDir("lnd", false)
	defaultTLSCertFilename = "tls.cert"
	defaultTLSCertPath     = filepath.Join(
		defaultLndDir, defaultTLSCertFilename,
	)

	defaultDataDir     = "data"
	defaultChainSubDir = "chain"

	defaultMacaroonDir = filepath.Join(
		defaultLndDir, defaultDataDir,
		defaultChainSubDir, "bitcoin", "mainnet",
	)

	defaultNet = "mainnet"
)

var (
	host = flag.String("host", "localhost:10009", "host of the target lnd node")

	tlsPath = flag.String("tlspath", defaultTLSCertPath, "path to the "+
		"TLS cert of the target lnd node")

	macaroonDir = flag.String("macdir", defaultMacaroonDir, "path to the "+
		"readonly macaroon for the target lnd node")

	network = flag.String("network", defaultNet, "the network the lnd "+
		"node is running on (default:mainnet)")
)

func main() {
	flag.Parse()

	// To start, we'll create a new gRPC client for the target lnd node.
	// This'll be our source for all the information of the target node.
	lndClient, err := lndclient.NewBasicClient(
		*host, *tlsPath, *macaroonDir, *network,
		lndclient.MacFilename("readonly.macaroon"),
	)
	if err != nil {
		log.Fatalf("unable to create client: %v", err)
	}

	// In order to check if any invalid channels are accepted we'll compare
	// the how big we think the channel is (our subjective view) to the
	// _actual_ size of the channel in lnd's local channel graph. The
	// channel graph has the correct channel values, but a node could be
	// tricked into setting the wrong value/outpoint where it stores the
	// actual channel data.
	//
	// So first we'll obtain all the node's current open channels to check
	// against the channel graph shortly below.
	channelResp, err := lndClient.ListChannels(
		context.Background(), &lnrpc.ListChannelsRequest{},
	)
	if err != nil {
		log.Fatalf("unable to obtain channels: %v", err)
	}

	log.Printf("Obtaining candidate set of invalidate channels...")

	// Now that we have our channels, we'll now construct our subjective
	// view of a channels existence as well as its total capacity.
	subjectiveChanView := make(map[lnwire.ShortChannelID]btcutil.Amount)
	for _, channel := range channelResp.Channels {
		cid := lnwire.NewShortChanIDFromInt(channel.ChanId)

		subjectiveChanView[cid] = btcutil.Amount(channel.Capacity)
	}

	log.Printf("Filtering out valid channels...")

	// Now that we have our subjective view of channels, we'll check
	// against the objective channel graph (properly reject invalid
	// channels and fully derives their full value from the chain) to see
	// if things match up. If they don't, then we've accepted a fake
	// channel.
	invalidChannels := make(map[lnwire.ShortChannelID]struct{})
	for cid, subjectiveSize := range subjectiveChanView {
		// Given a channel ID, we'll query the channel graph for the
		// actual information concerning that channel.
		graphChan, err := lndClient.GetChanInfo(
			context.Background(), &lnrpc.ChanInfoRequest{
				ChanId: cid.ToUint64(),
			})
		if err != nil {
			log.Printf("unable to obtain graph channel for "+
				"cid(%v): %v", cid, err)

			// If we can't find the channel in the channel graph,
			// then we assume that it's invalid.
			invalidChannels[cid] = struct{}{}
			continue
		}

		// This is where we hold our breath...
		//
		// If size of the channel from the PoV of the channel graph
		// doesn't match how big _we_ think the channel is, then it's
		// invalid.
		if graphChan.Capacity != int64(subjectiveSize) {
			log.Printf("**** FAKE CHANNEL FOUND ****")
			log.Printf("CID: %v")
			log.Printf("Actual channel value: %v", graphChan.Capacity)
			log.Printf("Subjective channel value: %v", subjectiveSize)
			log.Printf("****************************")

			invalidChannels[cid] = struct{}{}
		}
	}

	log.Printf("Num invalid channels found: %v", len(invalidChannels))

	// If no invalid channels were found (yay!!!), then we're done here.
	if len(invalidChannels) == 0 {
		log.Printf("Your node was not affected by CVE-2019-12999!")
		return
	}

	log.Printf("Quantifying amount lost due to forwards over invalid channels...")

	// At this point, we suspect that a channel is invalid. As a result,
	// we'll attempt to compute the total amount of coins that may have
	// been drained using the channel. To do that, we'll obtain the history
	// of all HTLCs successfully forwarded through this node.
	fwdHistoryReq := &lnrpc.ForwardingHistoryRequest{
		StartTime:    1,
		EndTime:      uint64(time.Now().Unix()),
		NumMaxEvents: math.MaxUint32,
	}
	forwardingHistory, err := lndClient.ForwardingHistory(
		context.Background(), fwdHistoryReq,
	)
	if err != nil {
		log.Fatalf("unable to obtain forwarding history: %v", err)
	}

	//
	chanForwardHistory := make(map[lnwire.ShortChannelID]btcutil.Amount)
	for _, fwdEvent := range forwardingHistory.ForwardingEvents {
		cidIn := lnwire.NewShortChanIDFromInt(fwdEvent.ChanIdIn)
		cidOut := lnwire.NewShortChanIDFromInt(fwdEvent.ChanIdOut)

		// If this forwarding event doesn't involve this channel, then
		// we'll skip it.
		_, incomingInvalidChan := invalidChannels[cidIn]
		_, outgoingInvalidChan := invalidChannels[cidOut]
		if !(incomingInvalidChan || outgoingInvalidChan) {
			continue
		}

		// Otherwise, if this was channel was used as the incoming
		// link, then this forward means we've lost the amount we
		// accepted inbound as well as the fee. These funds were lost
		// as we accepted "fake" coins on an incoming channel an
		// exchanged them for real coins on the outgoing channel. We
		// lose the fee as well since we thought we were keeping some
		// extra on the incoming channel. This wasn't the case.
		chanForwardHistory[cidIn] += -btcutil.Amount(
			fwdEvent.AmtIn - fwdEvent.Fee,
		)

		// If we ever completed a forward that went _out_ through this
		// channel, then we've gained funds as we exchanged real coins
		// (incoming) for fake coins.
		chanForwardHistory[cidOut] += btcutil.Amount(fwdEvent.AmtOut)
	}

	// Next, we'll print out each channel along with a breakdown for how
	// many coins were lost as a result of it.
	var totalLoss btcutil.Amount
	for chanID, amtLost := range chanForwardHistory {
		log.Printf("FakeChannel(%v) resulted in loss of: %v", chanID, amtLost)

		totalLoss += amtLost
	}

	// We'll then take the sum of net balances of each channel to produce
	// our calculation of the amount of coins lost.
	log.Printf("Amount lost: %v", totalLoss)
}
