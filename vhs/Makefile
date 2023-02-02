tapes = $(wildcard *.tape)
gifs = $(patsubst %.tape,gif/%.gif,$(tapes))

all: $(gifs)

gif/%.gif: %.tape
	@echo "Generating $@.gif"
	vhs $<

.PHONY: all
