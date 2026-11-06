#!/usr/bin/env python3
"""Add a verified FP32 MLX companion to a local model directory.

Requires numpy, torch, onnx and safetensors in an export environment only.
The application uses the native C++ engine and has no Python dependency.
Every checkpoint tensor must occur in the FP32 ONNX initializers, possibly
transposed; this refuses accidentally pairing weights from a different run.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path

ARCHITECTURE = "ettin-68m-v1"


def digest(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def validate_config(cfg):
    expected = dict(vocab_size=50368, hidden_size=512, intermediate_size=768,
                    num_hidden_layers=19, num_attention_heads=8, norm_eps=1e-5,
                    norm_bias=False, attention_bias=False, mlp_bias=False,
                    hidden_activation="gelu", local_attention=128)
    for key, value in expected.items():
        if cfg.get(key) != value:
            raise ValueError(f"unsupported {key}: {cfg.get(key)!r}, expected {value!r}")
    types = ["full_attention" if i % 3 == 0 else "sliding_attention" for i in range(19)]
    if cfg.get("layer_types") != types:
        raise ValueError("unsupported encoder attention layers")
    for kind in set(types):
        rope = cfg.get("rope_parameters", {}).get(kind, {})
        if rope.get("rope_theta") != 160000 or rope.get("rope_type") != "default":
            raise ValueError(f"unsupported {kind} rotary parameters")


def export(model_dir, checkpoint, config, task):
    import numpy as np
    import onnx
    from onnx import numpy_helper
    from safetensors.numpy import save_file
    import torch

    validate_config(json.loads(config.read_text()))
    manifest_path = model_dir / "manifest.json"
    manifest = json.loads(manifest_path.read_text())
    contract = "ids-attention-pool-v1" if task == "kestrel" else "ids-attention-pool-segments-v1"
    if manifest.get("input_contract") != contract:
        raise ValueError(f"{task} requires {contract}")
    source_name = manifest["files"]["onnx_fp32"]
    if Path(source_name).name != source_name:
        raise ValueError("ONNX filename must be local to model_dir")
    source = model_dir / source_name
    sha = digest(source)
    if sha != manifest["sha256"][source_name]:
        raise ValueError("FP32 ONNX checksum does not match manifest")
    graph = onnx.load(str(source))
    # Export is deliberately limited to the two validated architectures.
    expected_inputs = {"input_ids", "attention_mask", "pooling_mask"}
    expected_outputs = {"logits", "probs", "target_tokens"}
    if task == "jayhawk":
        expected_inputs.add("segment_ids")
        expected_outputs = {"verdict", "families", "sentences"}
    if {v.name for v in graph.graph.input} != expected_inputs or {v.name for v in graph.graph.output} != expected_outputs:
        raise ValueError("unexpected ONNX inputs or outputs")
    state = torch.load(str(checkpoint), map_location="cpu", weights_only=True)
    weights = {}
    def fingerprint(a):
        return (a.shape, hashlib.sha256(a.tobytes(order="C")).digest())
    originals = set()
    for tensor in graph.graph.initializer:
        value = numpy_helper.to_array(tensor)
        originals.add(fingerprint(value))
        if value.ndim == 2:
            originals.add(fingerprint(value.T))
    for name, tensor in state.items():
        if tensor.dtype != torch.float32:
            raise ValueError(f"{name}: expected unquantized FP32 checkpoint")
        value = np.ascontiguousarray(tensor.numpy())
        if fingerprint(value) not in originals:
            raise ValueError(f"{name}: checkpoint tensor does not match the FP32 ONNX export")
        weights[name] = value
    name = "model.mlx.safetensors"
    metadata = dict(format="pharos-mlx-v1", architecture=ARCHITECTURE,
                    task=task, source_onnx_sha256=sha)
    temporary = model_dir / (name + ".partial")
    save_file(weights, str(temporary), metadata=metadata)
    os.replace(temporary, model_dir / name)
    manifest["mlx"] = dict(metadata, weights=name)
    manifest["sha256"][name] = digest(model_dir / name)
    temporary_manifest = manifest_path.with_suffix(".partial")
    temporary_manifest.write_text(json.dumps(manifest, indent=2) + "\n")
    os.replace(temporary_manifest, manifest_path)
    return manifest["mlx"]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("model_dir", type=Path)
    parser.add_argument("--checkpoint", type=Path, required=True)
    parser.add_argument("--encoder-config", type=Path, required=True)
    parser.add_argument("--task", choices=["kestrel", "jayhawk"], required=True)
    args = parser.parse_args()
    print(json.dumps(export(args.model_dir, args.checkpoint, args.encoder_config, args.task), indent=2))


if __name__ == "__main__":
    main()
