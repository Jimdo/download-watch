# Jimdo / download-watch

`download-watch` is a utility to watch on URLs and save their output locally. Also it's possible to execute commands when files changed. For example you could reload a webserver when a new configuration was written.

It can save the file
- periodically no matter what
- when it changed (ETag aware) so that it's not written if the server says nothing changed
- when the SHA256 hash doesn't match locally but on the server

## Configuration file

```yaml
---
files:
  # Key for the map is the target file path
  /etc/myconfig.conf:
    # Optional: Specify user:pass for the basic authentication
    basic_auth: myuser:mypass
    # Optional: How long to wait for the file to finish downloading (default: 30s)
    timeout: 30s
    # Required: How long to wait between two downloads
    fetch_interval: 5m
    # Optional: Ignore ETag sent by server, refresh file even if it's the same
    ignore_etag: false
    # Optional: Check existing file / downloaded file against checksum
    sha256: e84712238709398f6d349dc2250b0efca4b72d8c2bfb7b74339d30ba94056b14
    # Required: URL to fetch the file from
    url: https://example.com/myconfig.conf
  /etc/myotherconfig.conf:
    url: https://example.com/myotherconfig.conf
    fetch_interval: 1h
```
