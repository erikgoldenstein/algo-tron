# Hosting a bot

Your bot has to stay connected to `tron.erik.gdn:4000` to keep playing. You
can leave it running on your own computer, put it on a small home server, or
rent a tiny server or container online.

The public ALGO-TRON server runs at Hetzner in Nürnberg, Germany. Hosting your
bot in Germany or nearby usually gives it the quickest connection, especially
late in a game when the board moves faster.

## Hosting on your own machine

A machine you already keep online can run the bot without a separate hosting subscription.

You do not need to change any router settings or open any ports. Just run the
bot as you would on your normal computer.

A Raspberry Pi, NAS, home server, old laptop, or even an Android phone with
Termux is easily powerful enough. Docker also works, but is not required.

## Hosted options

If you do not have an always-on machine at home, you can run the bot in a
container or on a virtual machine. Containers are usually easier; virtual
machines give you more control and can also run other projects.

Prices below are approximate. Tax, storage, location, and special offers can
change the final price. Free plans can also change or disappear, so check the
provider's website before signing up.

### Container hosting

Container hosts take care of most server setup for you. You normally connect a
Git repository or give them a Dockerfile, choose the start command, and let the
host keep the bot running.

| Provider | Price | Location | Tradeoff |
| --- | --- | --- | --- |
| [Northflank Sandbox](https://northflank.com/pricing) | Free | Provider-selected | Two free services that do not sleep; shared computing power may slow complicated bots. |
| [Back4app](https://www.back4app.com/pricing/container-as-a-service) | Free | United States | A free Docker container with no credit card required; it needs a small web health check and is farther from Nürnberg. |
| [Koyeb Eco Nano](https://www.koyeb.com/docs/reference/instances) | About USD 1.61/month | Frankfurt | Frankfurt location; do not choose its free web service, which goes to sleep. |
| [Fly.io](https://fly.io/docs/about/pricing/) | About USD 2--3/month | European regions available | Good Docker support; make sure the machine is configured to stay running. |
| [Northflank paid](https://northflank.com/pricing) | About USD 2.70/month | Provider-selected | Paid alternative to the Sandbox plan. |
| [Railway](https://railway.com/pricing) | USD 5/month minimum | European regions available | Git deployment; minimum spend may exceed one bot's usage. |
| [Render](https://render.com/pricing) | About USD 7/month | European regions available | Easy background workers; its free services cannot keep a bot running all day. |

Compare the free-plan restrictions and available locations before choosing a host.

### VM hosting

A virtual machine, or VM, is a small Linux computer in a data centre. You set
it up yourself just like a home server. This takes a little more work, but the
same VM can run several bots and other small projects.

| Provider | Price | Location | Tradeoff |
| --- | --- | --- | --- |
| [Oracle Cloud Always Free](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier.htm) | Free | Frankfurt when available | Frankfurt option; signup and free-machine availability vary. |
| [Google Cloud Free Tier](https://cloud.google.com/free/docs/free-cloud-features) | Free | Selected US regions | Reliable normal VM; limited monthly data and much farther from Nürnberg. |
| [Scaleway Stardust](https://www.scaleway.com/en/pricing/virtual-instances/) | About EUR 0.43/month, plus storage and tax | Europe | Availability varies. |
| [IONOS](https://www.ionos.de/server/vps) | About EUR 4/month after the introductory offer, plus setup | Germany available | Plenty of power; check the later price and contract before ordering. |
| [DigitalOcean](https://www.digitalocean.com/pricing/droplets) | About USD 4/month | Frankfurt available | VM hosting; costs more than some small containers. |
| [OVHcloud](https://www.ovhcloud.com/de/vps/) | About EUR 4.53/month including German VAT | Europe | Lots of space and a daily backup, but far more than one bot needs. |
| [Hetzner](https://www.hetzner.com/cloud/) | About EUR 6.53/month including German VAT | Nürnberg available | Same city as the game server; capacity exceeds one example bot's needs. |

Vultr, Linode/Akamai, netcup, Contabo, and many smaller VM providers work as
well. Compare the normal price after any special offer, the server location,
and whether you can cancel monthly.

## Temporary free offers

[AWS](https://aws.amazon.com/free/),
[Azure](https://azure.microsoft.com/free/), and other providers offer free
credits to new accounts. These work, but only until the credit or trial ends.
Set a billing warning and a reminder if you use one.

## Before you deploy

- Keep the password out of a public Git repository. Most hosts let you save it
  as a secret or environment variable.
- Make the bot reconnect its TCP socket when the connection is dropped, as the
  example bots do. This lets it reconnect automatically after the game server
  is restarted or redeployed.
- Turn on automatic restarts so the bot comes back after a crash or reboot.

## Help keep this page current

This list is community maintained. Free plans disappear, prices change, and
new hosts appear all the time. Contributions can add providers or update existing entries. If you spot something out of date,
please open an issue or pull request.
