# `chanleakcheck`

This a tool to check if your `lnd` node was targeted by CVE-2019-12999, which
was recently fully disclosed on. The tool will check if your node accepted an
invalid channel, as well as attempt to quantify the total amount of lost coins,
if any.

Note that this tool is only for `lnd`, however both `eclair` and `c-lightning`
also have tools for detection as well:

  * [eclair's tool](https://github.com/ACINQ/detection-tool-cve-2019-13000)
  * [c-lightning's tool](https://ozlabs.org/~rusty/clightning-checkchannels)

## Installation 

You can build the tool with
```
go build -mod=vendor -v
```

## Checking Your Node

Once the tool has been installed, you can check a target node with the
following command:
```
./chanleakcheck
```

If your node wasn't affected, then you should see something like:
```
2019/09/27 10:35:10 Your node was not affected by CVE-2019-12999!
```

Otherwise, a break down of each invalid channel along with the invalid forwards
will be shown.

The default execution of the command assumes the binary is being run from the
same machine as the target node, and the node is using default locations for
it's config/cert. Arguments of the tool have been provided to allow the tool to
check against a remote node:
```
   ./chanleakcheck -h
Usage of ./chanleakcheck:
  -host string
    	host of the target lnd node (default "localhost:10009")
  -macdir string
    	path to the readonly macaroon for the target lnd node (default "")
  -network string
    	the network the lnd node is running on (default:mainnet) (default "mainnet")
  -tlspath string
    	path to the TLS cert of the target lnd node (default "")
```
