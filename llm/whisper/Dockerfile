# vLLM serving Whisper (openai/whisper-large-v3-turbo): POST /v1/audio/transcriptions.
#
# The image tag is PINNED. The patch below is tied to vLLM 0.32.0 and is re-derived,
# not carried over, on any upgrade — `git apply` fails the build if the upstream source
# no longer matches, and the greps check the result. Drop it once
# vllm-project/vllm#53207 (or an equivalent) is in the release in use.
#
#   01 — make the `prompt` request field work. Upstream puts the prompt after a literal
#        `<|prev|>`, which is not a Whisper token, and then prepends
#        `<|startoftranscript|>` in front of it; a prompted request comes back as
#        garbage. The patch uses `<|startofprev|>` and leaves the decoder prompt as
#        built. Requests without a prompt are unchanged.
#
# Flags, measurements and the reasons: README.md, ../../docker.whisper.yaml.

FROM vllm/vllm-omni:v0.32.0

COPY patches/ /tmp/patches/
RUN cd /usr/local/lib/python3.12/dist-packages/vllm \
    && git apply --verbose /tmp/patches/01-whisper-prompt.patch \
    && grep -q '<|startofprev|> {request_prompt}' model_executor/models/whisper.py \
    && grep -q 'skip_decoder_start_token: bool = True' model_executor/models/whisper.py \
    && rm -rf /tmp/patches

ENV HF_HOME=/root/.cache/huggingface
EXPOSE 8008
