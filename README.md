# inner

Go CLI application managed with a multi-binary capable structure.

## Project Structure
- `cmd/`: Contains the main entry points. Each subdirectory represents a binary.
- `internal/`: Private library code specifically for this project.
- `pkg/`: Public library code that can be used by other projects.

## Getting Started
Build the project using the Makefile:
```bash
make build
```
The executable will be located in `/$(BINARY_DIR)`.

```sh
# Install the Cobra library as a dependency
go get -u github.com/spf13/cobra@latest

# Install the Cobra CLI generator tool to your $GOPATH/bin
go install github.com/spf13/cobra-cli@latest

# Create the directory for the 'bar' binary
mkdir -p cmd/bar

# Initialize Cobra specifically for the 'bar' executable
# We point the package name to the subdirectory
cobra-cli init --pkg-name github.com/foo/bar/cmd/bar ./cmd/bar
```
