# Shipping logs to Elasticsearch

The service writes four structured JSON log files to `logs/`:

| File | One line per | Embeds |
|---|---|---|
| `api.log` | HTTP request | full request + response, `thirdparty[]`, `trace[]`, `trace_id` |
| `consumer.log` | consumed message | `thirdparty[]`, `trace[]`, `trace_id` |
| `thirdparty.log` | outbound third-party call | method/url/status/latency + bodies |
| `trace.log` | custom trace point (`AddTrace`) | name + data |

`thirdparty` and `trace` entries are written **twice**: embedded in the parent
`api`/`consumer` record *and* to their own files, all sharing the same `request_id`
and `trace_id` so you can correlate them with the APM trace.

## Run Filebeat locally

```bash
filebeat -c deploy/filebeat.yml -e
```

## Local Elasticsearch + Kibana + Filebeat (docker-compose)

```yaml
services:
  elasticsearch:
    image: docker.elastic.co/elasticsearch/elasticsearch:8.14.0
    environment:
      - discovery.type=single-node
      - xpack.security.enabled=false
    ports: ["9200:9200"]

  kibana:
    image: docker.elastic.co/kibana/kibana:8.14.0
    depends_on: [elasticsearch]
    ports: ["5601:5601"]

  filebeat:
    image: docker.elastic.co/beats/filebeat:8.14.0
    user: root
    command: ["filebeat", "-e", "-strict.perms=false"]
    environment:
      - ELASTICSEARCH_HOSTS=http://elasticsearch:9200
    volumes:
      - ./deploy/filebeat.yml:/usr/share/filebeat/filebeat.yml:ro
      - ./logs:/logs:ro
      - /var/lib/filebeat:/usr/share/filebeat/data
    depends_on: [elasticsearch]
```

Indices are created per category: `wapgo-api-*`, `wapgo-consumer-*`,
`wapgo-thirdparty-*`, `wapgo-trace-*`.
