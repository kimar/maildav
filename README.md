# maildav

Fork of [targodan/maildav](https://github.com/targodan/maildav).

IMAP polling service that uploads attachments to a WebDav server.

## Usage using Docker

1. Create `config.yml` (see `config.example.yml` as a reference) and configure IMAP and WebDav servers as you wish.
2. Run service using Docker Compose: `docker compose up -d`.