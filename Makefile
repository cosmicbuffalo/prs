.PHONY: build test vet fmt format install uninstall clean

BIN := prs
CMD := ./cmd/prs
INSTALL_DIR := $(HOME)/.local/share/prs
BIN_DIR := $(HOME)/.local/bin
GO := $(shell test -x /usr/local/go/bin/go && echo /usr/local/go/bin/go || echo go)

build:
	$(GO) build -ldflags "-s -w" -o $(BIN) $(CMD)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

format:
	gofmt -w .

install: build
	mkdir -p $(INSTALL_DIR)/bin $(BIN_DIR)
	cp $(BIN) $(INSTALL_DIR)/bin/$(BIN).new
	mv -f $(INSTALL_DIR)/bin/$(BIN).new $(INSTALL_DIR)/bin/$(BIN)
	ln -sf $(INSTALL_DIR)/bin/$(BIN) $(BIN_DIR)/$(BIN)
	@echo ""
	@echo "Installed prs to $(BIN_DIR)/$(BIN)"

uninstall:
	rm -f $(BIN_DIR)/$(BIN)
	rm -rf $(INSTALL_DIR)

clean:
	rm -f $(BIN)
