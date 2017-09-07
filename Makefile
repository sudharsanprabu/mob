build:
	mkdir bin
	go build -o ./bin/client client/client.go
	go build -o ./bin/tracker tracker/tracker.go

clean:
	rm -rf bin
