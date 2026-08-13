# Remote Config Service

An example web service that provides machine configuration for Talos Linux files based on hardware identifiers.

## Run

To run the pre-built container use

```bash
mkdir data

docker run --rm -t -v $PWD/data:/app/data:ro \
  -p 8080:8080 ghcr.io/siderolabs/talos-remote-config:latest
```

In a separate terminal run [`booter`](https://github.com/siderolabs/booter) to PXE boot machines configured to use your host as a remote config endpoint.

```bash
docker run --rm --network host \
  ghcr.io/siderolabs/booter:v0.3.0 \
    --extra-kernel-args talos.config=${TALOS_REMOTE_CONFIG}:8080/metadata?h=${hostname}&m=${mac}&s=${serial}&u=${uuid}
```

Now put machine config or partial machine config in folders that represent variable names used in the config parameters.

```bash
mkdir -p data/{h,m,s,u}
```

PXE boot a machine on the network and watch the talos-remote-config logs for requests. When a machine connects you'll see the variables values in the logs. You can use that to create the appropriate configuration files.

If you want to apply the same config to all machines you can use the `-common-config` or `-default-config` options.

### Directory Structure

The data directory should contain subdirectories named after each parameter:

```
data/
├── m/                    # MAC address files (parameter: m)
├── h/                    # Hostname files (parameter: h)
├── s/                    # Serial number files (parameter: s)
├── u/                    # UUID files (parameter: u)
└── foo/                  # Custom parameter files (parameter: foo)
```

### File Naming

Within each parameter folder:
- Files should be named after the parameter value (e.g., `server01.yaml`, `aa-bb-cc-11-22-33.yaml`)
- Colons in values are converted to hyphens for filesystem compatibility
- Supported extensions: `.yaml`, `.yml`
- Can use directories with multiple YAML files instead of a single file

### Examples

1. **Individual files**: `data/h/server01.yaml`
2. **Directories with multiple files**: `data/u/550e8400-e29b-41d4-a716-446655440000/`
   - `base.yaml`
   - `apps.yaml`
   - etc.
3. **Custom parameters**: `data/foo/bob.yaml` (matches `?foo=bob`)

### MAC Address Partial Matching

The `m` parameter has special handling with partial matching from most specific to most general.

For MAC address `aa:bb:cc:11:22:33`, it searches in the `data/m/` folder in order:
1. `m/aa-bb-cc-11-22-33.yaml` (exact match)
2. `m/aa-bb-cc-11-22.yaml` (5 octets)
3. `m/aa-bb-cc-11.yaml` (4 octets)
4. `m/aa-bb-cc.yaml` (3 octets - vendor prefix)
5. `m/aa-bb.yaml` (2 octets)
6. `m/aa.yaml` (1 octet)

The service returns the first match found.

## Command-line Flags

- `-data-dir`: Directory containing metadata files (default: `./data`)
- `-port`: Port to listen on (default: `8080`)
- `-default-config`: Default configuration for unmatched requests
- `-common-config`: Configuration to combine with every request
- `-ip-acl-enabled`: Enable IP-based access control (default: `false`)
- `-ip-acl-config`: Path to IP-to-metadata mapping YAML file (required when `-ip-acl-enabled=true`)
- `-ip-acl-forwarded`: HTTP header name to resolve client IP (e.g., `X-Forwarded-For`; empty means use socket address)

## IP-based Authentication

When enabled, the service validates that incoming requests originate from known IP addresses and that the metadata parameters sent by each request match what is expected for that IP. This prevents unauthorized nodes from requesting another node's configuration.

### Enabling

```bash
docker run --rm -t -v $PWD/data:/app/data:ro \
  -v $PWD/ip-map.yaml:/app/ip-map.yaml:ro \
  -p 8080:8080 ghcr.io/siderolabs/talos-remote-config:latest \
  -ip-acl-enabled=true -ip-acl-config /app/ip-map.yaml
```

### Mapping File Format

The mapping file is a YAML file that maps IP addresses to their expected metadata values. Values are stored in normalized form (colons replaced with hyphens, lowercase). Only the parameters defined for an IP will be validated; additional parameters sent by the client are ignored.

```yaml
# /etc/talos/ip-map.yaml
10.0.1.5:
  h: web-01
  m: aa-bb-cc-dd-ee-ff
  s: SN001
10.0.2.3:
  u: 550e8400-e29b-41d4-a716-446655440000
10.0.3.7:
  h: worker-01
  m: 11-22-33-44-55-66
```

### Validation Rules

- **Unknown IP** (not in mapping): Returns `401 Unauthorized`
- **Known IP, parameter value mismatch**: Returns `403 Forbidden`
- **Known IP, no recognized parameters**: Returns `403 Forbidden`
- **Partial identity**: If the mapping defines `h`, `m`, and `s` but the client only sends `?m=...`, authentication passes and config is served for the MAC parameter only

### Reverse Proxy Support

When behind a reverse proxy or load balancer, configure the header that carries the original client IP:

```bash
-ip-acl-enabled=true -ip-acl-config /app/ip-map.yaml -ip-acl-forwarded "X-Forwarded-For"
```

The service reads the first entry from the specified header (for chained proxies) and falls back to the socket address if the header is not present.

### Hot-reload

The mapping file is watched for changes at runtime. When the file is modified, it is automatically reloaded. If a malformed file replaces a valid one, all requests will receive `401` until a valid mapping is restored.

## API

### Endpoint: `/metadata`

Accepts any query parameters. Common parameters:
- `h`: Hostname
- `m`: MAC address (supports partial matching)
- `s`: Serial number
- `u`: UUID
- Any custom parameter (e.g., `foo=bob`)

All parameters are optional and can be URL-escaped.

### Example Requests

```bash
# Request by MAC address
curl "http://localhost:8080/metadata?m=aa:bb:cc:11:22:33"

# Request by hostname
curl "http://localhost:8080/metadata?h=server01"

# Request by UUID
curl "http://localhost:8080/metadata?u=550e8400-e29b-41d4-a716-446655440000"

# Multiple parameters
curl "http://localhost:8080/metadata?h=server01&m=aa:bb:cc:11:22:33"

# URL-escaped MAC address
curl "http://localhost:8080/metadata?m=aa%3Abb%3Acc%3A11%3A22%3A33"

# Custom arbitrary parameter
curl "http://localhost:8080/metadata?foo=bob"

# Serial number
curl "http://localhost:8080/metadata?s=SN12345678"
```


## Response Format

When multiple files match, they are combined with YAML document separators (`---`):

```yaml
machine
  network:
    kubespan:
      enabled: true
---
apiVersion: v1alpha1
kind: HostnameConfig
hostname: server01
auto: off
```

## Example Data Structure

```
data/
├── m/                                      # MAC address parameter
│   ├── aa-bb-cc-11-22-33.yaml             # Specific MAC
│   ├── aa-bb-cc-11-22.yaml                # Partial MAC match
│   └── aa-bb-cc.yaml                      # Vendor prefix match
├── h/                                      # Hostname parameter
│   └── server01.yaml                      # Hostname match
├── s/                                      # Serial number parameter
│   └── SN12345678.yaml                    # Serial number match
├── u/                                      # UUID parameter
│   └── 550e8400-e29b-41d4-a716-446655440000/  # UUID directory
│       ├── base.yaml
│       └── apps.yaml
└── foo/                                    # Custom parameter
    └── bob.yaml                            # Custom value (matches ?foo=bob)
```

## Error Handling

- Returns `401 Unauthorized` when IP-based authentication is enabled and the source IP is not in the mapping file
- Returns `403 Forbidden` when IP-based authentication is enabled but parameter validation fails (mismatch or no recognized parameters)
- Returns `404 Not Found` if no matching files are found
- Returns `500 Internal Server Error` if YAML validation fails
- Logs errors for individual file read failures but continues processing other files
- Skips parameters whose folders don't exist in the data directory

## Testing

Start the server with the example data:

```bash
./remote-config -data-dir ./data

# Test specific MAC match
curl "http://localhost:8080/metadata?m=aa:bb:cc:11:22:33"

# Test partial MAC match (should match aa-bb-cc-11-22.yaml)
curl "http://localhost:8080/metadata?m=aa:bb:cc:11:22:99"

# Test vendor prefix match (should match aa-bb-cc.yaml)
curl "http://localhost:8080/metadata?m=aa:bb:cc:99:88:77"

# Test hostname
curl "http://localhost:8080/metadata?h=server01"

# Test UUID with directory
curl "http://localhost:8080/metadata?u=550e8400-e29b-41d4-a716-446655440000"

# Test serial number
curl "http://localhost:8080/metadata?s=SN12345678"

# Test custom parameter
curl "http://localhost:8080/metadata?foo=bob"
```

## Build

See `make help` for build targets.

