# DiscoPanel

<div align="center">
  <img src="web/discopanel/static/g1_256x256.png" alt="DiscoPanel" width="128" height="128" />
  
  **The Minecraft server manager that works**
  
  [Website](https://discopanel.app) | [Gallery](https://discopanel.app#gallery) | [Discord](https://discord.gg/6Z9yKTbsrP) | [Docker Hub](https://github.com/discohaus/discopanel/pkgs/container/discopanel)
</div>

---

## What is this?

DiscoPanel is a web-based Minecraft server + proxy + modpack manager. Built by someone who was tired of bloated control panels that require a PhD to operate and still manage to break at the worst possible moment.

## Why DiscoPanel?

Because managing Minecraft servers shouldn't be difficult:

- **Docker-powered** - Each server runs in its own container. No more "works on my machine" disasters
- **Multi-server** - Run vanilla, modded, different versions, whatever. They won't fight each other
- **Smart Proxy** - Players connect through custom hostnames. No more port gymnastics (though basic ports assignment is still available)
- **Modpack Support** - Native CurseForge integration that actually downloads the mods/modpacks you tell it to
- **Web UI** - Clean interface that doesn't look like it crawled out of 2003
- **Auto-everything** - Auto-start, auto-stop, auto-pause. Set it and forget it
- **Easily extensible** - With a proto-based API (see proto/discopanel/v1), you can easily generate an api client within your own project

## Quick Start

```bash

# Non-exhaustive list of requirements for building from source:
# 1. Go (v1.24.5 if that matters)
# 2. NodeJs + npm (for building front end)

# Clone it
git clone https://github.com/discohaus/discopanel
cd discopanel

# Generate the rpc/api code for server/client using buf in docker
docker run --rm -v "$(pwd):/workspace" -w /workspace -u "$(id -u):$(id -g)" bufbuild/buf:latest generate

# Get npm deps and build frontend first
cd web/discopanel && npm install && npm run build && cd ../..

# Build backend and embed front end
go build -o discopanel cmd/discopanel/main.go

# Run it
./discopanel

# Open it
# http://localhost:8080
```

> For development, just install `make` (on ubuntu/deb, `sudo apt install make`) and run `make gen` + `make dev` after the above `git clone` and `cd` step. Super easy!

## Docker Run

```bash

docker run -d \
  --name discopanel \
  --restart unless-stopped \
  --network host \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v ./data:/app/data \
  -v ./backups:/app/backups \
  -v ./tmp:/app/tmp \
  -v ./config.yaml:/app/config.yaml:ro \
  -e DISCOPANEL_DATA_DIR=/app/data \
  -e DISCOPANEL_HOST_DATA_PATH="$(pwd)/data" \
  -e TZ=UTC \
  ghcr.io/discohaus/discopanel:latest
```

## Docker Compose (Recommended)

```yaml

services:
  discopanel:
    image: ghcr.io/discohaus/discopanel:latest
    container_name: discopanel
    restart: unless-stopped

    # Option 1 (RECOMENDED FOR SIMPLICITY): Use host network mode
    network_mode: host

    # Option 2 (MORE COMPLICATED, ONLY USE IF YOU NEEDED): Use bridge mode with port mapping (default)
    #
    # NOTE: Only specify minecraft server ports (25565 ... etc) for proxied minecraft servers using a hostname. 
    #       Discopanel will automatically expose ports needed on the managed minecraft server instances. In other
    #       words, only the discopanel web port is needed + proxy port(s).
    # ports:
    #   - "8080:8080"         # DiscoPanel web interface
    #   - "25565:25565"       # Minecraft port/proxy-port
    #   - "25565-25665:25565-25665/tcp" # Additional ports/proxy-ports if needed
    #   - "25565-25665:25565-25665/udp" # Also map UDP for some Minecraft features

    volumes:
      # Docker socket for managing containers
      # NOTE FOR FEDORA/RHEL/CENTOS/ETC.: SE Linux requires :z to be added as a suffix to volume mounts. EX: - /var/run/docker.sock:/var/run/docker.sock:z
      - /var/run/docker.sock:/var/run/docker.sock
  
      # IMPORTANT: This is where your server(s) data will be stored on the host.
      # You can set this to any path you'd like, but the path must exist AND you must use the same
      # absolute paths below for the below env vars (in the environment section at the bottom). Example:
      # DISCOPANEL_DATA_DIR=/app/data
      # DISCOPANEL_HOST_DATA_PATH=/home/user/data
      # (See environment)
      - /home/user/data:/app/data

      - ./backups:/app/backups
      - ./tmp:/app/tmp
      
      # Configuration file, uncomment if you are using a config file (optional, see config.example.yaml for all available options).
      #- ./config.yaml:/app/config.yaml:ro
    environment:
      - DISCOPANEL_DATA_DIR=/app/data

      # IMPORTANT: THIS MUST BE SET TO THE SAME PATH AS THE SERVER DATA PATH IN "volumes" above
      - DISCOPANEL_HOST_DATA_PATH=/home/user/data
      - TZ=UTC

    # DONT FORGET THIS
    extra_hosts:
      - "host.docker.internal:host-gateway"

```

>> NOTE: Prebuilt binaries coming soon... but just use docker, you'll need it anyways. Ask for help in discord, we'd love to help.

## Features That Actually Matter

### Server Management
- Create servers in seconds with any Minecraft version
- Support for Forge, Fabric, Paper, Spigot, and every other mod loader that exists
- Live console access and log streaming
- RCON support for remote commands
- Automatic Java version selection (no more version hell, unless you are into that)

### Proxy System
- Can be enabled / disabled depending on your preference (disabled by default)
- Automatic routing based on hostname
- Multiple proxy listeners for different use cases
- Custom hostnames for each server (`survival.yourserver.com`, `creative.yourserver.com`)

>> NOTE: DNS needs a wildcard A record, like `*.yourserver.com` -> your IP

- Just one open port is required. No port forwarding nightmares

>> NOTE: With just the default proxy port 25565:25565 forwarded, you can host a virtually unlimited amount of servers

### Modpack Integration
- Direct CurseForge modpack installation
- Automatic mod downloading and updates
- Server pack support for easier distribution
- Manual mod uploads when automation fails

### Resource Management
- Per-server memory limits
- JVM flag optimization (Aikar's flags included)
- Automatic cleanup of orphaned containers
- Detached mode for persistent servers

### Security
- Can be enabled / disabled depending on your preference (disabled by default)
- Built-in user authentication system with role-based access
- Admin, Editor, and Viewer roles
- Recovery key system (because passwords get forgotten)
- Session management and JWT tokens

## FAQ

#### Please look here if you have any issues with running Discopanel or Minecraft servers through Discopanel.
<details>
  <summary>
    Failed to start server: [failed_precondition] server container not created or 'ERROR: Failed to create container: failed to create container: Error response from daemon: invalid mount config for type "bind": bind source path does not exist:'
  </summary>
  This is usually due to a permissions issue. Discopanel runs minecraft server containers as PUID:GID 1000:1000 by default so if that user or group does not have access to the directory that Discopanel is installed in, then that will cause this issue. Using `ls -a` to show permissions on files and directories, `chown` to change ownership of files and directories and `chmod` to change the permissions of files and directories will be very helpful for troubleshooting and solving the issue on a Linux host.
</details>

<details>
  <summary>
    Anything related to fuego, Curseforge API or other issues
  </summary>
  These are likely not something that we can help with unfortunately. Either your DNS is not working properly, Curseforge's API is having issues or your connection to Curseforge's servers are otherwise hindered, for example, by a firewall.
</details>

## Configuration

DiscoPanel uses a `config.yaml` file. Here's what matters:

```yaml
storage:
  data_dir: "./data/servers"
  backup_dir: "./data/backups"

proxy:
  enabled: true
  base_url: "minecraft.example.com"
  listen_ports: [25565]
```

>> NOTE: There are a metric ton worth of configurable settings for your DiscoPanel and the servers it hosts, they can all be setup here ahead of time

## Requirements

- Docker (obviously)
- Go 1.24.5+ (only if building from source)
- A functioning brain (optional but recommended)

## API

DiscoPanel has a full REST API if you're into that sort of thing:

```bash
# List servers
curl http://localhost:8080//discopanel.v1.ServerService/ListServers

# Create a server
curl -X POST http://localhost:8080/discopanel.v1.ServerService/CreateServers \
  -H "Content-Type: application/json" \
  -d '{"name":"My Server","mc_version":"1.20.1","mod_loader":"vanilla"}'

# Start a server
curl -X POST http://localhost:8080/discopanel.v1.ServerService/RestartServer \
-data '{ "id": "${id}"}'
```

>> NOTE: See the API section in the left sidebar of the Discopanel WebUI for all the routes, or join the discord and ask about it!

## Contributing

Found a bug? Want a feature? Open an issue or submit a PR. Just don't make it worse.

### Generating Proto Code

Proto files live in `proto/`. After making changes, regenerate Go and TypeScript code:

```bash
# With make (requires Docker)
make gen

# or

# Without make (...also requires Docker)
docker run --rm -v "$(pwd):/workspace" -w /workspace bufbuild/buf:latest generate
```

## Docs

The doc site lives in `docs/discopanel/` and is built with [Astro](https://astro.build) + [Starlight](https://starlight.astro.build). Deployed to [docs.discopanel.app](https://docs.discopanel.app).

### Contributing, building, & viewing locally

```bash
make dev-docs
```

Open `http://localhost:4321`.

### Reporting doc issues

If something is wrong or missing, open an issue on [GitHub](https://github.com/discohaus/discopanel/issues) or mention it in [Discord](https://discord.gg/6Z9yKTbsrP).

## License

MIT. Do whatever you want with it, just don't blame me when it breaks.

## Support

- [Discord](https://discord.gg/6Z9yKTbsrP) - Come complain directly
- [GitHub Issues](https://github.com/discohaus/discopanel/issues) - For the brave
