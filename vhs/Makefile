tapes = $(wildcard *.tape)
gifs = $(patsubst %.tape,gif/%.gif,$(tapes))

all: $(gifs)

gif/%.gif: %.tape common.tape
	@echo "Generating $@"
	vhs $<

.PHONY: all
