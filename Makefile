PHONY: all build run clean test ui ui-build                                                                                                     
                                                                                                                                                 
# Default target                                                                                                                                 
all: ui-build build run                                                                                                                          
                                                                                                                                                 
# Build the Go server                                                                                                                            
build:                                                                                                                                           
    go build -o bin/asset-server main.go                                                                                                         
                                                                                                                                                 
# Run the server                                                                                                                                 
run:                                                                                                                                             
    ./bin/asset-server                                                                                                                           
                                                                                                                                                 
# Clean build artifacts                                                                                                                          
clean:                                                                                                                                           
    rm -rf bin/                                                                                                                                  
    rm -rf ui/dist/                                                                                                                              
                                                                                                                                                 
# Run Go tests                                                                                                                                   
test:                                                                                                                                            
    go test ./...                                                                                                                                
                                                                                                                                                 
# Start UI development server                                                                                                                    
ui:                                                                                                                                              
    cd ui && bun run dev                                                                                                                         
                                                                                                                                                 
# Build UI for production                                                                                                                        
ui-build:                                                                                                                                        
    cd ui && bun install                                                                                                                         
    cd ui && bun run build 
	                                                                                                                                                                                                                                                              
all: ui-build build run

# Build the Go server
build:
	go build -o bin/asset-server

# Run the server
run:
	./bin/asset-server

# Clean build artifacts
clean:
	rm -rf bin/
	rm -rf ui/dist/

# Run Go tests
test:
	go test ./...

# Start UI development server
ui:
	cd ui && bun run dev

# Build UI for production
ui-build:
	cd ui && bun install
	cd ui && bun run build

# Install UI dependencies
install-deps:
	cd ui && bun install
