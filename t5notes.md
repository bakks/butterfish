# Notes on T5 branch

- I did some experimentation with running t5 models in Butterfish. Summary: I got it basically working (on CPU), it was interesting exercise but probably not worth more effort. I haven't tried to fine-tune or anything, just using existing t5 parameters at face value.

- Big takeaways:

  - Now that we have ChatGPT less powerful models feel pretty underwhelming.
  - ONNX is actually a good model platform but requires wrapping / glue code. It doesn't seem as up to date with the latest stuff as you would want (e.g. maybe it should have tokenization functionality built in, it doesn't seem to do inference loops), but is in the neighborhood.
  - It does seem theoretically possible to have a well-trained and minified model running locally and doing useful things but the current-gen T5 stuff is not that.
  - Desert island - The idea of training models specifically for local use and knowledge of the local computer. What if every Mac shipped with an LLM that was trained on commands for that MacOS version? What would be the most useful LLM to have on a computer with no internet access? What would you want it to do?

- T5

  - The output of public Google research that has been fully open-sourced (unlike the top of the line PaLM models).
  - Comes in a variety of sizes small, base, large, xl, xxl.
  - The Hugging Face transformers library is pretty robust, i.e. they provide decent Python tools for working with it.

- T5 Testing

  - Based on testing, T5 is most usef for language translation and summarization. It's not really useful for code generation like GPT.
  - I got this working locally, tried it in the Hugging Face python library, a tried it with a [Replicate implementation](https://replicate.com/devxpy/flan-t5).
  - Quality ranges based on which size model you use. The small ones are pretty brain dead but the bigger ones are not great either relative to GPT.

- ONNX

  - I chose to try using ONNX for inference because it appears to be a good platform for managing models and running inference, it theoretically supports CoreML for M1/M2 machines, there's good support for exporting models to ONNX, and because I wanted to learn more about it for other projects.
  - Exporting was actually pretty painless! The script is in `./exportt5.py`. This will produce `.onnx` model files and some tokenizer information. Caveats below.
  - Important learning: the state of the ONNX export world is that generally in Pytorch/TF/JAX there is a bunch of Python glue code around the real computation, which gets put into an operator DAG. It's that operator DAG that's exported, so to make a `.onnx` model work you have to add a bunch of wrapping.
  - I've uploaded these to...
  - The default homebrew package doesn't compile in CoreML support. I've created [this custom package](https://github.com/bakks/homebrew-bakks/blob/main/onnxruntime.rb) to workaround. You can install with `brew install bakks/bakks/onnxruntime`.
  - This works in theory but I haven't actually tested/confirmed coreML activation, I've been mucking around on CPU.
  - I wrote some golang/onnx glue code in `./onnx`.

- Implementation
  - I implemented T5 tokenization and inference (using the `.onnx` model files) in Go based on https://github.com/praeclarum/transformers-js, which was the most succinct implementation I could find. It actually mostly works.
  - Current implementation is pretty inefficient.

Running the t5 code, this will require some experimentation.

```
brew install bakks/bakks/onnxruntime

git clone github.com/bakks/butterfish
cd butterfish
git checkout t5
make

# Get onnx exports from https://huggingface.co/datasets/bakks/flan-t5-onnx and put in ~/Library/butterfish
# Check - you should thus have a file like ~/Library/butterfish/flan-t5-small/encoder_model.onnx


./bin/butterfish -m 'flan-t5-base' prompt 'Translate english to german: Is this thing working?'
./bin/butterfish -m 'flan-t5-base' --coreml prompt 'Translate english to german: Is this any faster?'
```
