# Docksmith - neo (CLI + Parser)

## Completed Work

* Implemented CLI with `build` command
* Parsed Docksmithfile into structured instructions
* Added validation for instructions
* Supported `-t` flag for image tagging

## Build the Executable

```bash
go build -o docksmith cmd/main.go
```

## How to Run

```bash
./docksmith build -t myapp:latest .
```

## Output Example

Building image with tag: myapp:latest
Parsed Instructions:

1. FROM ubuntu
2. WORKDIR /app
3. COPY . .
4. RUN go build -o app
5. CMD ["./app"]

## For Svojas

Use this function:
parser.ParseDocksmithfile("Docksmithfile")

It returns structured instructions you can use to build layers.
