# splay

splay is http request tool. http request scenario can be defined by yaml.
It can specify request count, throughput, and validate status code.
It runs scenario data concurrently.

## Scenario Example

```yaml
scenarios:
  - name: ping
    url: https://google.com
    # throughput's mean request count per 1 second
    throughput: 1
    # you can specify period(second) or specify count
    period: 600
    validates:
    - name: status_code=200
      status_code: 200
```

```bash
splay
```

