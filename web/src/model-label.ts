// "Unknown model" is the stored model of usage whose source recorded none
// (internal/archive/parallelism.go, usage_table.go and others). It is a
// pricing key and a filter value, so it never changes; modelLabel is what
// people read instead. A model family is the lower-cased model
// (modelFamily in usage_table.go), so its sentinel is "unknown model".
export const unknownModel = "Unknown model";
export const modelNotRecorded = "Model not recorded";
export const modelLabel = (model: string) => model === unknownModel || model === unknownModel.toLowerCase() ? modelNotRecorded : model;
// The fields that hold a model or a model family: model, root_model,
// model_family, root_model_family, main_model and the like.
export const isModelField = (field: string) => /(^|_)model(_family)?$/.test(field);
// A field's value as people read it, when the field holds a model.
export const modelFieldLabel = (field: string, value: string) => isModelField(field) ? modelLabel(value) : value;
