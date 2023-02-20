from pathlib import Path
import transformers as t
from transformers import AutoTokenizer, pipeline
from optimum.onnxruntime import ORTModelForSeq2SeqLM

# print out the version of the transformers library
print("transformers version:", t.__version__)



models = [
    #"google/flan-t5-small",
    #"google/flan-t5-base",
    #"google/flan-t5-large",
    "google/flan-t5-xl",
    "google/flan-t5-xxl",
]

for model_id in models:
    model_name = model_id.split("/")[1]
    onnx_path = Path("onnx/" + model_name)

# load vanilla transformers and convert to onnx
    model = ORTModelForSeq2SeqLM.from_pretrained(model_id, from_transformers=True)
    tokenizer = AutoTokenizer.from_pretrained(model_id)

# save onnx checkpoint and tokenizer
    model.save_pretrained(onnx_path)
    tokenizer.save_pretrained(onnx_path)
