# IP.8080.LI

A simple HTTP server that returns the visitor's IP address in Plain Text, JSON, JSONP or XML format along with optional geographic information.

## Features
- Supports IPv4 and IPv6
- Returns the visitor's IP address in Plain Text, JSON, JSONP or XML format
- Response optionally includes flag, continent, country, region, city, postal code, coordinates, timezone, and ASN details
- No API key required. No ratelimit
- 100% Free and Open Source

## Usage

**The below APIs support programmatic access without any limits.**

#### IP ONLY

- `curl https://ip.8080.li` Alternatively, visit <https://ip.8080.li>
- `curl https://ip.8080.li?format=json` Alternatively, visit <https://ip.8080.li?format=json>
- `curl https://ip.8080.li?format=jsonp` Alternatively, visit <https://ip.8080.li?format=jsonp>
- `curl https://ip.8080.li?format=jsonp&callback=customFn` Alternatively, visit <https://ip.8080.li?format=jsonp&callback=customFn>
- `curl https://ip.8080.li?format=xml` Alternatively, visit <https://ip.8080.li?format=xml>

#### IP + GEO

- `curl https://ip.8080.li/geo` Alternatively, visit <https://ip.8080.li/geo>
- `curl https://ip.8080.li/geo?format=json` Alternatively, visit <https://ip.8080.li/geo?format=json>
- `curl https://ip.8080.li/geo?format=jsonp` Alternatively, visit <https://ip.8080.li/geo?format=jsonp>
- `curl https://ip.8080.li/geo?format=jsonp&callback=customFn` Alternatively, visit <https://ip.8080.li/geo?format=jsonp&callback=customFn>
- `curl https://ip.8080.li/geo?format=xml` Alternatively, visit <https://ip.8080.li/geo?format=xml>

#### Search Other IP

- `curl https://ip.8080.li/?ip=8.8.8.8` Alternatively, visit <https://ip.8080.li/?ip=8.8.8.8>
- `curl https://ip.8080.li/?ip=8.8.8.8&format=json` Alternatively, visit <https://ip.8080.li/?ip=8.8.8.8&format=json>
- `curl https://ip.8080.li/?ip=8.8.8.8&format=jsonp` Alternatively, visit <https://ip.8080.li/?ip=8.8.8.8&format=jsonp>
- `curl https://ip.8080.li/?ip=8.8.8.8&format=jsonp&callback=customFn` Alternatively, visit <https://ip.8080.li/?ip=8.8.8.8&format=jsonp&callback=customFn>
- `curl https://ip.8080.li/?ip=8.8.8.8&format=xml` Alternatively, visit <https://ip.8080.li/?ip=8.8.8.8&format=xml>

---

## Development (Bun)

```bash
# clone this project
git clone https://github.com/P1N2O/ip.8080.li.git

# navigate to the project directory
cd ip.8080.li

# copy and set env variables
cp .env.example .env

# install dependencies
bun i

# start dev server
bun dev
```

## Deployment (Docker)

```bash
# clone this project
git clone https://github.com/P1N2O/ip.8080.li.git

# navigate to the project directory
cd ip.8080.li

# copy and set env variables
cp .env.example .env

# build and start container
docker compose up -d --build
```

## LICENSE
[MIT License](LICENSE)