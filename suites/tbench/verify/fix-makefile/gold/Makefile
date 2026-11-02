SRC = $(sort $(wildcard src/*.txt))

all: dist/bundle.txt

dist/bundle.txt: $(SRC)
	mkdir -p dist
	cat $(SRC) > dist/bundle.txt

clean:
	rm -rf dist
