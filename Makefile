.PHONY: test cover

test:
	go -C backend test -race -shuffle=on -count=1 -timeout=60s ./...

cover:
	go -C backend test -count=1 -coverprofile=../cover.out ./... && go -C backend tool cover -func=../cover.out
