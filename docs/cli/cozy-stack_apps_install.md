## cozy-stack apps install

Install an application with the specified slug name from the given source URL.

### Synopsis


Install an application with the specified slug name from the given source URL.

```
cozy-stack apps install [slug] [sourceurl]
```

### Examples

```
$ cozy-stack apps install --domain cozy.local:8080 files 'git://github.com/cozy-files-v3.git#build'
```

### Options inherited from parent commands

```
      --admin-host string   administration server host (default "localhost")
      --admin-port int      administration server port (default 6060)
      --all-domains         work on all domains iterativelly
  -c, --config string       configuration file (default "$HOME/.cozy.yaml")
      --domain string       specify the domain name of the instance
      --host string         server host (default "localhost")
      --log-level string    define the log level (default "info")
  -p, --port int            server port (default 8080)
```

### SEE ALSO
* [cozy-stack apps](cozy-stack_apps.md)	 - Interact with the cozy applications

