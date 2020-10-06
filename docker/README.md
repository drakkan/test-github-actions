# Official Docker images

SFTPGo provides official Docker images. They are available [here](https://github.com/users/drakkan/packages/container/package/sftpgo).

## Start a SFTPGo server instance

Starting a SFTPGo instance is simple:

```shell
docker run --name some-sftpgo -p 127.0.0.1:8080:8080 -p 2022:2022 -d "ghcr.io/drakkan/sftpgo:edge"
```

Now visit [http://localhost:8080/](http://localhost:8080/) and create a new SFTPGo user. The SFTP service is available on port 2022.

## LOG

The logs are available through Docker's container log:

```shell
docker logs some-sftpgo
```

## Configuration

The runtime configuration can be customized via environment variables that you can set passing the `-e` option to the `docker run` command or inside the `environment` section if you are using [docker stack deploy](https://docs.docker.com/engine/reference/commandline/stack_deploy/) or [docker-compose](https://github.com/docker/compose).

Please take a look [here](../docs/full-configuration.md#environment-variables) to learn how to configure SFTPGo via environment variables.

## Where to Store Data

Important note: There are several ways to store data used by applications that run in Docker containers. We encourage users of the SFTPGo images to familiarize themselves with the options available, including:

- Let Docker manage the storage for SFTPGo data by writing them to disk on the host system using its own internal [volume management](https://docs.docker.com/engine/tutorials/dockervolumes/#adding-a-data-volume). This is the default and is easy and fairly transparent to the user. The downside is that the files may be hard to locate for tools and applications that run directly on the host system, i.e. outside containers.
- Create a data directory on the host system (outside the container) and mount this to a directory visible from inside the container. This places the SFTPGo files in a known location on the host system, and makes it easy for tools and applications on the host system to access the files. The downside is that the user needs to make sure that the directory exists, and that e.g. directory permissions and other security mechanisms on the host system are set up correctly. The SFTPGo images run using `1000` as uid and gid.

The Docker documentation is a good starting point for understanding the different storage options and variations, and there are multiple blogs and forum postings that discuss and give advice in this area. We will simply show the basic procedure here for the latter option above:

1. Create a data directory on a suitable volume on your host system, e.g. `/my/own/sftpgodata`.
2. Start your SFTPGo container like this:

```shell
docker run --name sftpgo_edge \
    -p 127.0.0.1:8080:8090 \
    -p 2022:2022 \
    --mount type=bind,source=/my/own/sftpgodata,target=/var/lib/sftpgo \
    -e SFTPGO_HTTPD__BIND_PORT=8090 \
    -d "ghcr.io/drakkan/sftpgo:edge"
```

The `--mount type=bind,source=/my/own/sftpgodata,target=/var/lib/sftpgo` part of the command mounts the `/my/own/sftpgodata` directory from the underlying host system as `/var/lib/sftpgo` inside the container, where SFTPGo will store its data.
