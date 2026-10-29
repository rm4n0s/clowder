# Tutorial: run Clowder for your team

This tutorial takes you from an empty Linux server to a Clowder that your team uses every day: served over HTTPS at your own address, with the first admin and the first members signed up, SSH sign-in codes and video calls working, a nightly backup, a restore you have tested, and a way to upgrade. It takes about an hour.

It follows one path: Docker Compose with PostgreSQL, and Caddy on the server for HTTPS. Each part ends with a check, so you know it worked before you go on. The [README](../README.md) is the reference for everything here and for the paths this tutorial does not take (the bare binary, SQLite, nginx, several processes); the tutorial links to its sections where they say more.

Throughout, `chat.example.org` is your Clowder's address and `203.0.113.7` is your server's public IP address. Replace them with your own.

## What you need

- **A Linux server** (amd64 or arm64) with a public IP address and 2 GB of memory or more. You log in to it over SSH and can run `sudo`.
- **A domain name** with an `A` record (and `AAAA` for IPv6) that points `chat.example.org` to the server. Caddy needs it to get a TLS certificate.
- **Docker with the Compose plugin**, version 2.24.4 or later (for `!override` in Part 2). `docker compose version` tells you.
- **git**, and **Caddy** (Part 4).
- **Open ports:** 80 and 443 (TCP) for Caddy; 2222 (TCP) if you want SSH sign-in codes; 7882 (UDP **and** TCP) if you want calls.

## Part 0: try it on your laptop first (optional, 5 minutes)

To see Clowder before you install it, run it with demo data on your own machine. This needs Go 1.27 and no Docker:

```sh
git clone https://github.com/rm4n0s/clowder.git
cd clowder
CGO_ENABLED=0 go build -o clowder ./cmd/clowder
./clowder demo -data /tmp/clowder-demo
./clowder serve -data /tmp/clowder-demo
```

Open <http://localhost:8080> and log in as `admin` with the password `demo-password-1234` (`alice`, `bob` and `carol` have the same one). Look around: the rooms, a post and its comments, a room's board, Messages, and the admin pages under the avatar menu. Stop the server with Ctrl-C and delete `/tmp/clowder-demo` when you are done. Never run the demo on a real installation: its passwords are public.

## Part 1: get Clowder and configure it

On the server, put Clowder in `/opt/clowder` and check out the release:

```sh
sudo git clone https://github.com/rm4n0s/clowder.git /opt/clowder
sudo chown -R "$USER": /opt/clowder
cd /opt/clowder
git checkout v1.2.0
```

Make the settings file and keep it private (it will hold the database password):

```sh
cp .env.example .env
chmod 600 .env
openssl rand -hex 24      # prints a random password; copy it
```

Edit `.env` and change these lines; leave the rest as they are:

```sh
CLOWDER_DB_PASSWORD=<the random password you just made>
CLOWDER_BASE_URL=https://chat.example.org
CLOWDER_TRUST_PROXY=true
CLOWDER_MEDIA_PUBLIC_IP=203.0.113.7
```

- `CLOWDER_DB_PASSWORD`: compose refuses to start with no password, and the example value is public, so never keep it.
- `CLOWDER_BASE_URL`: the address people type. Invitation links are made from it, and because it starts with `https://` the login cookies are marked `Secure`.
- `CLOWDER_TRUST_PROXY=true`: Caddy will add the visitor's address to each request, and Clowder will take it from there, so that the limit of 5 failed logins in 15 minutes counts each visitor on its own. Turn it on only because, after Part 2, nothing but Caddy can reach Clowder.
- `CLOWDER_MEDIA_PUBLIC_IP`: the address the browsers send the media of a call to. Without it, calls connect only between people on the server's own network.

`.env.example` explains every variable, and the README's [Configuration](../README.md#configuration) lists every setting.

## Part 2: let only Caddy reach the web port

`compose.yaml` publishes the web port 8080 on every interface of the server. Behind a reverse proxy it must be reachable only from the server itself, or anyone could skip HTTPS and fake the address that `CLOWDER_TRUST_PROXY` trusts. Make `/opt/clowder/compose.override.yaml` (compose reads it by itself, next to `compose.yaml`):

```yaml
services:
  clowder:
    ports: !override
      - "127.0.0.1:${CLOWDER_PORT:-8080}:8080"
      - "${CLOWDER_SSH_PORT:-2222}:2222"
      - "${CLOWDER_MEDIA_PORT:-7882}:7882/udp"
      - "${CLOWDER_MEDIA_PORT:-7882}:7882/tcp"
```

`!override` replaces the whole list of ports, so the list must name every port you want. The SSH port (2222) and the media port (7882) stay published on every interface, because people reach them directly, not through Caddy. If you do not want SSH sign-in codes, delete the 2222 line; if you do not want calls, delete both 7882 lines. A port that is not published is closed, whatever the firewall says.

Check what compose will publish:

```sh
docker compose config | grep -A1 host_ip
```

It must print `host_ip: 127.0.0.1` followed by `target: 8080`, and nothing else: the web port is the only one bound to the server itself.

## Part 3: start Clowder

```sh
docker compose up -d --build
```

The first build takes a few minutes. Then:

```sh
docker compose ps
```

Wait until both `postgres` and `clowder` say `healthy`. Ask Clowder itself:

```sh
curl -fsS http://127.0.0.1:8080/healthz      # prints: ok
```

If a service is not healthy, `docker compose logs clowder` shows why. A setting that is not valid stops Clowder at the start with a message that names the setting.

## Part 4: HTTPS with Caddy

Install Caddy on the server: `sudo apt install caddy` on Debian and Ubuntu, or follow <https://caddyserver.com/docs/install>. Replace `/etc/caddy/Caddyfile` with:

```
chat.example.org {
	encode zstd gzip
	reverse_proxy 127.0.0.1:8080 {
		# send each event of a live stream at once
		flush_interval -1
	}
}
```

```sh
sudo systemctl reload caddy
```

Caddy gets a certificate for `chat.example.org` by itself, passes the `Host` header unchanged (Clowder refuses forms whose origin is not its own host) and adds the visitor's address to `X-Forwarded-For`. `flush_interval -1` matters: every page keeps a live stream open, and a proxy that buffers it delays every update.

**Check:** open <https://chat.example.org>. You see Clowder's login page, with a valid certificate. To use nginx instead, see [Running behind a TLS reverse proxy](../README.md#running-behind-a-tls-reverse-proxy).

## Part 5: the first admin

Nobody can sign up without an invitation, so the command line makes the first one:

```sh
docker compose run --rm clowder invite -admin
```

It prints a link like `https://chat.example.org/invite/<token>` and when it expires. Open it in your browser and choose:

- a **username**: 3 to 32 characters of `a-z`, `0-9`, `_` and `-`. You log in with it, and people mention you with `@` and the username;
- a **display name**, which people see;
- a **password** of at least 12 characters.

You are now logged in as an admin. The link worked once and is now used up.

## Part 6: set up the team

**Make a room.** Open <https://chat.example.org/rooms/new>, give it a name (for example "general") and a description, and set *Visibility* to **Visible**. Everyone sees a visible room, reads it and joins it with one click. Only admins make visible rooms; anyone can make a private room, which only the people added to it see.

**Invite people.** Open the admin page (<https://chat.example.org/admin/users>). Under *Invite someone* choose the role **member** and press *Make an invitation link*, then *Copy link*. Each link signs up one person and expires after 7 days. Clowder sends no email: you hand the link over yourself, in a channel you trust. Anyone who has the link can use it.

**Check:** send yourself an invitation, open it in a private window and sign up as a member. Join "general" and write a post. Your admin window shows it at once, without a reload. That is the live stream through Caddy working.

What you will do on the admin page later:

- **Users:** *Make admin* / *Make member*, *Disable* (logs the person out everywhere; their posts stay), *Reset password* (a one-time link for 24 hours that you hand over) and *Turn off SSH code* (Part 7). The last admin cannot lose the role.
- **Rooms:** rename or delete any room, and remove members.
- **Audit log:** who made, changed or deleted posts, comments, tasks, board columns, rooms and room memberships, and the changes to accounts (passwords, reset links, SSH keys and codes). It covers private rooms too, and nothing can edit or delete it.

If the last admin forgets their password, the command line makes a reset link:

```sh
docker compose run --rm clowder reset-password -user <username>
```

## Part 7: SSH sign-in codes (optional)

People who keep an SSH key can add a second factor to their login: a one-time code that only the owner of a registered key can fetch. The compose file already runs the code server on port 2222. Try it as your own user:

1. On your computer, print your public key: `cat ~/.ssh/id_ed25519.pub`.
2. In Clowder open your avatar › Settings › **Sign-in**, give the key a name, paste the line and press *Add key* (it asks for your password).
3. Fetch a code with the command the page shows:

   ```sh
   ssh -p 2222 <username>@chat.example.org
   ```

   The first time, ssh asks you to trust the host key: compare its fingerprint with the one on the settings page. You get `Your Clowder sign-in code: K7QF-9MZP (valid for 60 seconds)`.
4. Turn on **Ask for an SSH code when I log in**. From now on, after the right password, the login asks for a fresh code.

**Check:** log out, log in again, fetch a code with `ssh` and type it in.

When someone loses their key, turn the code off for them under Admin › Users › *Turn off SSH code*; they can then log in with the password alone. Two things to know: a password reset link logs in without the code, and the host key (`ssh_host_ed25519_key` in the data volume) is in every backup, so its fingerprint survives a restore. More in [SSH sign-in codes](../README.md#ssh-sign-in-codes).

## Part 8: calls (optional)

The two people of a direct conversation can call each other with video or audio. The browsers send the media to the Clowder server on port 7882, which forwards it to the other person, so no STUN or TURN server is needed. The media does not pass through Caddy: the browsers connect straight to that port, over UDP or, where a network blocks UDP, over TCP. That is why both protocols must be open and `CLOWDER_MEDIA_PUBLIC_IP` must be the address they reach the server at.

**Check:** with two accounts (your admin and the member from Part 6, in two browsers or on two devices) open *Messages*, start a conversation with the other person only, and press **Video call** in its header. The other side rings; answer, and both of you should see and hear each other. If the call rings but never connects, port 7882 is closed for UDP and TCP somewhere (Part 9), or `CLOWDER_MEDIA_PUBLIC_IP` is wrong. The camera works only over `https://`, which Part 4 gives you. More in [Calls](../README.md#calls).

## Part 9: the firewall

Open only what you use:

| Port | Protocol | For |
|---|---|---|
| 22 | TCP | Your own SSH login to the server |
| 80, 443 | TCP | Caddy (80 only to get and renew the certificate and redirect to HTTPS) |
| 2222 | TCP | SSH sign-in codes, if you use them |
| 7882 | UDP and TCP | Calls, if you use them |

With `ufw`:

```sh
sudo ufw allow 22/tcp
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw allow 2222/tcp
sudo ufw allow 7882/udp
sudo ufw allow 7882/tcp
sudo ufw enable
```

**Know this about Docker:** the ports that Docker publishes bypass `ufw` and `firewalld`, because Docker writes its own packet-filter rules. So the list of ports in `compose.override.yaml` (Part 2) is what really decides who reaches the containers, and the firewall rules above matter mostly for Caddy and SSH. A firewall in front of the server (your hosting provider's) does cover Docker's ports: open the same ports there. Never publish PostgreSQL (5432); compose does not.

## Part 10: nightly backups

A backup is two files: the database, saved with `pg_dump`, and an archive of the attachments and the SSH host key, made by `clowder backup`. **Run `pg_dump` first**: then every attachment that the dump names is in the archive. Both files hold every message, post, password hash and file: keep them as safely as the server.

Make `/usr/local/sbin/clowder-backup` (`sudo` to write it, then `sudo chmod 700 /usr/local/sbin/clowder-backup`):

```sh
#!/bin/sh
# Back up Clowder: the database first, then the attachments and the host key.
set -eu
umask 077
cd /opt/clowder
dir=/var/backups/clowder
stamp=$(date -u +%Y-%m-%dT%H%MZ)
mkdir -p "$dir"

docker compose exec -T postgres sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "$dir/.clowder-$stamp.dump"
docker compose run --rm -T clowder backup -o - > "$dir/.clowder-$stamp.tar.gz"
mv "$dir/.clowder-$stamp.dump" "$dir/clowder-$stamp.dump"
mv "$dir/.clowder-$stamp.tar.gz" "$dir/clowder-$stamp.tar.gz"

# keep two weeks, and clear what failed runs left
find "$dir" -name 'clowder-*' -mtime +14 -delete
find "$dir" -name '.clowder-*' -mtime +1 -delete
```

The two files get their final names only when both are complete, so a failed run leaves only hidden temporary files. Run it once by hand:

```sh
sudo /usr/local/sbin/clowder-backup
ls -l /var/backups/clowder
```

`clowder backup` prints, on the terminal, what it saved: the counts of users, rooms, posts, messages and files. Then run it every night, from `/etc/cron.d/clowder-backup` (owned by root, mode 644, ending with a newline):

```
30 3 * * * root /usr/local/sbin/clowder-backup
```

A backup on the same disk does not survive the loss of the server: copy `/var/backups/clowder` to another machine with the tool you already trust (`rsync`, `restic`, `borg`), encrypted if it leaves your network.

## Part 11: test a restore

A backup you have never restored is a hope, not a backup. Restore the latest one into a second, throw-away compose project, on other ports, next to the real one:

```sh
cd /opt/clowder
b=/var/backups/clowder/clowder-<stamp>       # the latest pair, without the extension
export CLOWDER_PORT=8090 CLOWDER_SSH_PORT=127.0.0.1:2223 CLOWDER_MEDIA_PORT=127.0.0.1:7892
export CLOWDER_BASE_URL=http://localhost:8090
t="docker compose -p clowder-restore-test"

$t up -d --wait postgres
$t exec -T postgres sh -c 'pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --clean --if-exists' < "$b.dump"
$t run --rm -T clowder restore -i - -data /data < "$b.tar.gz"
$t up -d --wait
curl -fsS http://127.0.0.1:8090/healthz      # prints: ok
```

`-p clowder-restore-test` gives the test its own containers and volumes, and the variables you export win over `.env`. With `compose.override.yaml` from Part 2, they also publish every port of the test on `127.0.0.1` only, so nobody outside reaches the copy. `clowder restore` checks every file of the archive against its manifest before it writes anything, and refuses a directory that is not empty.

**Check:** from your computer, open a tunnel to the test's web and SSH ports, `ssh -L 8090:127.0.0.1:8090 -L 2223:127.0.0.1:2223 <you>@203.0.113.7`, then open <http://localhost:8090> and log in with your usual account. Your rooms, posts and attachments are there. If you turned on the SSH code, fetch it from the copy with `ssh -p 2223 <username>@localhost`: its host key fingerprint is the same as the real server's, because the key was in the archive.

Then remove the test, with its volumes:

```sh
$t down -v
unset CLOWDER_PORT CLOWDER_SSH_PORT CLOWDER_MEDIA_PORT CLOWDER_BASE_URL
```

To restore for real after a disaster, set up a new server with Parts 1, 2 and 4 (with the same `.env`), skip Part 3, and run the same steps without `-p`, the exports and the tunnel: Clowder must start only after the database and the archive are restored. More in [Backups](../README.md#backups).

## Part 12: upgrades

Clowder migrates its database by itself when a new version starts, and only adds to it. Downgrading is not supported, so take a backup first:

```sh
sudo /usr/local/sbin/clowder-backup
cd /opt/clowder
git fetch --tags
git checkout <new-tag>            # the new release, such as v1.3.0
docker compose up -d --build
docker compose ps                 # wait for healthy
```

Read the release's notes in the README's [Upgrading](../README.md#upgrading) section before you start: a new feature that listens on a port of its own is off until you turn it on. If the new version does not start, `docker compose logs clowder` says why; to go back, check out the old tag and restore the backup you took (Part 11, without `-p`).

## Everyday work

- **Logs:** `docker compose logs -f clowder`. They are JSON lines with one entry per request; they never hold passwords, tokens or post bodies.
- **Stop and start:** `docker compose stop` and `docker compose start`. `docker compose down` removes the containers and keeps the data; **`docker compose down -v` deletes the database and the attachments**.
- **A forgotten password:** Admin › Users › *Reset password*, and hand over the link.
- **Someone leaves:** Admin › Users › *Disable*. Their sessions end at once; their posts stay.
- **Health:** `GET /healthz` answers `ok` while the database answers; the container's own health check asks it every 10 seconds.
- **Metrics:** for Prometheus, remove the `#` in front of `CLOWDER_METRICS_ADDR` in `compose.yaml` and scrape `clowder:9090` from the compose network. The listener has no login: never publish it. See [Metrics](../README.md#metrics).

## Where next

- [README](../README.md): every setting, the bare binary with SQLite, PostgreSQL outside Docker, nginx, and the limits.
- [Several processes](../README.md#several-processes): two Clowder processes behind a load balancer, with Redis, for restarts without a pause.
- [docs/SECURITY.md](SECURITY.md): what was checked in each release and the risks that were accepted.
- [docs/PERFORMANCE.md](PERFORMANCE.md): how much memory and time Clowder needs as your team grows.
- [docs/API.md](API.md): for the people who write desktop clients.
