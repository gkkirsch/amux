.PHONY: build test test-claude install clean

build:
	go build -o amux .

test:
	go test ./... -v -count=1

test-claude:
	AMUX_TEST_CLAUDE=1 go test ./... -v -count=1 -run TestClaude

install: build
	install -m 0755 amux $(HOME)/.local/bin/amux

clean:
	rm -f amux
