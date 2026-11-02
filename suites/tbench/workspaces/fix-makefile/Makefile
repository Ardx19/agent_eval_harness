SRC = $(wildcard src/*.txt)

all: dist/bundle.txt

dist/bundle.txt: $(SRC)
	cat $(SRC) > dist/bundle.txt

clean:
	rm -rf dist
