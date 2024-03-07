# maildav

Fork of [targodan/maildav](https://github.com/targodan/maildav).

IMAP polling service that uploads attachments to a WebDav server.

## Usage using Docker

1. Copy the [config.example.yml](config.example.yml) and adjust to your needs:

```
$ cp config.example.yml config.yml
```

2. Run service using Docker Compose: 
```
$ docker compose up -d
```