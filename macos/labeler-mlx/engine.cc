// Native FP32 Ettin classifiers. Keep this outside the Go package so CPU
// builds have no compile-time dependency on the MLX SDK.
#include "engine.h"
#include <mlx/mlx.h>
#include <cmath>
#include <cstring>
#include <memory>
#include <stdexcept>
#include <unordered_set>

namespace mx = mlx::core;
using mx::array;
using Arrays = std::vector<array>;

namespace {
struct Model {
    std::unordered_map<std::string, array> weights;
    std::unordered_set<std::string> expected;
    bool kestrel;
    std::function<Arrays(const Arrays&)> forward;

    void require(const std::string& name, mx::Shape shape) {
        const auto it = weights.find(name);
        if (it == weights.end() || it->second.shape() != shape || it->second.dtype() != mx::float32)
            throw std::runtime_error("missing or incompatible FP32 weight: " + name);
        expected.insert(name);
    }
    void linear_shape(const std::string& p, int in, int out, bool bias = false) {
        require(p + ".weight", {out, in});
        if (bias) require(p + ".bias", {out});
    }
    Model(const char* path, const char* task, const char* source) : kestrel(std::string(task) == "kestrel") {
        if (!kestrel && std::string(task) != "jayhawk") throw std::runtime_error("unknown MLX task");
        mx::set_default_device(mx::Device::gpu);
        // MLX's default buffer cache scales with the Mac's total memory. Mixed
        // batch shapes otherwise retain tens of GB of unused Metal buffers.
        // Match the manager's 3 GiB GPU reservation (a soft evaluation limit)
        // and keep only a small reusable pool alongside the model's weights.
        mx::set_memory_limit(size_t(3072) * 1024 * 1024);
        mx::set_cache_limit(size_t(512) * 1024 * 1024);
        auto loaded = mx::load_safetensors(path);
        auto& meta = loaded.second;
        if (meta["format"] != "pharos-mlx-v1" || meta["architecture"] != "ettin-68m-v1" ||
            meta["task"] != task || meta["source_onnx_sha256"] != source)
            throw std::runtime_error("MLX weights metadata does not match the FP32 model");
        weights = std::move(loaded.first);
        require("enc.embeddings.tok_embeddings.weight", {50368, 512});
        require("enc.embeddings.norm.weight", {512});
        require("enc.final_norm.weight", {512});
        for (int i = 0; i < 19; ++i) {
            auto p = "enc.layers." + std::to_string(i);
            if (i) require(p + ".attn_norm.weight", {512});
            require(p + ".mlp_norm.weight", {512});
            linear_shape(p + ".attn.Wqkv", 512, 1536);
            linear_shape(p + ".attn.Wo", 512, 512);
            linear_shape(p + ".mlp.Wi", 512, 1536);
            linear_shape(p + ".mlp.Wo", 768, 512);
        }
        if (kestrel) linear_shape("head", 512, 270, true);
        else for (auto p : {"verdict", "fams", "sent"}) {
            linear_shape(std::string(p) + ".0", std::string(p) == "sent" ? 1536 : 1024, 512, true);
            linear_shape(std::string(p) + ".2", 512, std::string(p) == "sent" ? 2 : std::string(p) == "fams" ? 24 : 3, true);
        }
        if (expected.size() != weights.size()) throw std::runtime_error("unexpected MLX weights");
        Arrays values;
        for (auto& [_, v] : weights) values.push_back(v);
        mx::eval(values);
        forward = mx::compile(std::function<Arrays(const Arrays&)>([this](const Arrays& inputs) { return predict(inputs); }));
    }
    array linear(const array& x, const std::string& p) {
        auto y = mx::matmul(x, mx::transpose(weights.at(p + ".weight")));
        auto bias = weights.find(p + ".bias");
        return bias == weights.end() ? y : y + bias->second;
    }
    array norm(const array& x, const std::string& p) {
        return mx::fast::layer_norm(x, weights.at(p + ".weight"), std::nullopt, 1e-5f);
    }
    static array gelu(const array& x) {
        return x * array(0.5f) * (array(1.0f) + mx::erf(x * array(float(M_SQRT1_2))));
    }
    static array rotate(const array& x, const array& c, const array& s) {
        auto halves = mx::split(x, 2, -1);
        return x * c + mx::concatenate({-halves[1], halves[0]}, -1) * s;
    }
    static array select(const array& x, int index, int axis) {
        mx::Shape start(x.ndim(), 0), stop = x.shape();
        start[axis] = index;
        stop[axis] = index + 1;
        return mx::squeeze(mx::slice(x, start, stop), axis);
    }
    array encode(const array& ids, const array& att) {
        int b = ids.shape(0), s = ids.shape(1);
        auto h = norm(mx::take(weights.at("enc.embeddings.tok_embeddings.weight"), ids, 0), "enc.embeddings.norm");
        auto pos = mx::arange(s);
        auto key_ok = mx::astype(mx::reshape(att, {b, 1, 1, s}), mx::bool_);
        auto distance = mx::abs(mx::reshape(pos, {s, 1}) - mx::reshape(pos, {1, s}));
        // A finite floor also handles fully masked padding queries in SDPA.
        auto full = mx::where(key_ok, array(0.0f), array(-1e9f));
        auto local = mx::where(mx::logical_and(key_ok, mx::less_equal(distance, array(64))), array(0.0f), array(-1e9f));
        auto freq = mx::reshape(mx::astype(pos, mx::float32), {s, 1}) *
            mx::reshape((array(1.0f) / mx::power(array(160000.0f), mx::arange(0, 64, 2, mx::float32) / array(64.0f))), {1, 32});
        auto angles = mx::concatenate({freq, freq}, -1);
        auto c = mx::reshape(mx::cos(angles), {1, 1, s, 64});
        auto sin = mx::reshape(mx::sin(angles), {1, 1, s, 64});
        for (int i = 0; i < 19; ++i) {
            auto p = "enc.layers." + std::to_string(i);
            auto x = i ? norm(h, p + ".attn_norm") : h;
            auto qkv = mx::reshape(linear(x, p + ".attn.Wqkv"), {b, s, 3, 8, 64});
            auto q = rotate(mx::transpose(select(qkv, 0, 2), {0, 2, 1, 3}), c, sin);
            auto k = rotate(mx::transpose(select(qkv, 1, 2), {0, 2, 1, 3}), c, sin);
            auto v = mx::transpose(select(qkv, 2, 2), {0, 2, 1, 3});
            x = mx::fast::scaled_dot_product_attention(q, k, v, 0.125f, "", i % 3 ? local : full);
            h = h + linear(mx::reshape(mx::transpose(x, {0, 2, 1, 3}), {b, s, 512}), p + ".attn.Wo");
            auto gates = mx::split(linear(norm(h, p + ".mlp_norm"), p + ".mlp.Wi"), 2, -1);
            h = h + linear(gelu(gates[0]) * gates[1], p + ".mlp.Wo");
        }
        return norm(h, "enc.final_norm");
    }
    Arrays predict(const Arrays& in) {
        int b = in[0].shape(0), s = in[0].shape(1);
        auto h = encode(in[0], in[1]);
        auto pool = mx::astype(mx::reshape(in[2], {b, s, 1}), mx::float32);
        if (kestrel) {
            pool = pool * mx::reshape(in[1], {b, s, 1});
            auto pooled = mx::sum(h * pool, 1) / mx::maximum(mx::sum(pool, 1), array(1.0f));
            auto logits = mx::reshape(linear(pooled, "head"), {b, 5, 54});
            auto probs = array(0.5f) * (mx::sigmoid(select(logits, 4, 1)) + mx::sigmoid(select(logits, 0, 1)));
            return {logits, probs, mx::astype(mx::sum(in[2] * in[1], 1), mx::float32)};
        }
        auto cls = select(h, 0, 1);
        auto anchor = mx::sum(h * pool, 1) / mx::maximum(mx::sum(pool, 1), array(1.0f));
        auto onehot = mx::astype(mx::equal(mx::reshape(mx::clip(in[3], array(0), array(16)), {b, s, 1}),
                                         mx::reshape(mx::arange(1, 17), {1, 1, 16})), mx::float32);
        auto counts = mx::expand_dims(mx::maximum(mx::sum(onehot, 1), array(1.0f)), -1);
        auto sentences = mx::matmul(mx::transpose(onehot, {0, 2, 1}), h) / counts;
        auto z = mx::concatenate({cls, anchor}, -1);
        auto verdict = mx::softmax(linear(gelu(linear(z, "verdict.0")), "verdict.2"), -1);
        auto families = mx::sigmoid(linear(gelu(linear(z, "fams.0")), "fams.2"));
        auto features = mx::concatenate({sentences, mx::broadcast_to(mx::expand_dims(anchor, 1), {b, 16, 512}),
                                        mx::broadcast_to(mx::expand_dims(cls, 1), {b, 16, 512})}, -1);
        return {verdict, families, mx::sigmoid(linear(gelu(linear(features, "sent.0")), "sent.2"))};
    }
};
void* open_model(const char* path, const char* task, const char* source, char** error) {
    try {
        if (!mx::is_available(mx::Device::gpu)) {
            *error = strdup("unavailable: MLX Metal GPU is unavailable");
            return nullptr;
        }
        return new Model(path, task, source);
    }
    catch (const std::exception& e) { *error = strdup(e.what()); return nullptr; }
}
int run_model(void* handle, const int32_t* ids, const int32_t* att, const int32_t* pool,
              const int32_t* seg, int b, int s, float* output, char** error) {
    try {
        Model& model = *static_cast<Model*>(handle);
        if (b < 1 || b > PHAROS_MLX_MAX_BATCH || s < 1 || s > (model.kestrel ? 512 : 768) || int64_t(b) * s > 16384)
            throw std::runtime_error("MLX batch exceeds row, sequence or 16384 padded token limit");
        Arrays in{array(ids, {b, s}), array(att, {b, s}), array(pool, {b, s})};
        if (!model.kestrel) in.emplace_back(seg, mx::Shape{b, s});
        auto values = model.forward(in);
        // reshape copies noncontiguous views before reading their data.
        for (auto& v : values) v = mx::contiguous(v);
        mx::eval(values);
        for (const auto& v : values) {
            const float* data = v.data<float>();
            for (size_t i = 0; i < v.size(); ++i)
                if (!std::isfinite(data[i])) throw std::runtime_error("non-finite MLX output");
            std::memcpy(output, data, v.nbytes());
            output += v.size();
        }
        return 0;
    } catch (const std::exception& e) { *error = strdup(e.what()); return -1; }
}
void close_model(void* handle) {
    delete static_cast<Model*>(handle);
    mx::clear_cache();
}
const pharos_mlx_api api{open_model, run_model, close_model};
}
extern "C" const pharos_mlx_api* pharos_mlx_api_v1() { return &api; }
// Device check only: do not load weights, allocate model buffers or run inference.
extern "C" int pharos_mlx_available_v1() {
    try { return mx::is_available(mx::Device::gpu) ? 1 : 0; }
    catch (...) { return 0; }
}
