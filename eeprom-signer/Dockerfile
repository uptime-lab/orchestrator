# eeprom-signer: isolated container that holds the private signing key and the
# rpi-eeprom toolchain, and produces per-device signed EEPROM packages on request.
# The key, firmware bases and rpi-eeprom tools are MOUNTED at runtime (not baked
# into the image): /keys (ro), /eeprom-build (ro), /output (rw; the backend serves
# it to devices behind a signed capability).
FROM python:3.15-slim@sha256:65a65fc7b30b06cb6df4930fd4650697cb23f4c2698ab81fc87e4b031bc36928

# rpi-eeprom-config / rpi-eeprom-digest need python3 + openssl; sha256sum is in
# coreutils; bash for sign.sh.
# rpi-eeprom-digest needs xxd; rpi-eeprom-config --pubkey needs the Cryptodome
# module (pycryptodomex provides the `Cryptodome` namespace).
RUN apt-get update \
    && apt-get install -y --no-install-recommends bash openssl coreutils xxd \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY requirements.txt /app/requirements.txt
RUN pip install --no-cache-dir --require-hashes -r /app/requirements.txt
COPY sign.sh /app/sign.sh
COPY server.py /app/server.py
RUN chmod +x /app/sign.sh /app/server.py

ENV OUTPUT_ROOT=/output \
    SIGN_SCRIPT=/app/sign.sh \
    EEPROM_DIR=/eeprom-build \
    PRIVKEY=/keys/privkey.pem \
    PUBKEY=/keys/pubkey.pem \
    PORT=9099

EXPOSE 9099

# Run unprivileged. This process holds the signing key and shells out to the
# rpi-eeprom toolchain; there is nothing here that needs root. uid 1000 matches
# what the mounted /output directory is chowned to on the host.
RUN useradd --uid 1000 --create-home --shell /usr/sbin/nologin signer
USER 1000:1000

# Build provenance. GIT_REVISION is the commit the source came from and is
# supplied by the build (`--build-arg GIT_REVISION=<commit>`, which the
# release workflow passes for the tagged commit); it defaults to
# "unknown" so a build without it still succeeds and says so.
#
# Same label as backend/ and frontend/: without it a running signer image
# cannot answer "which commit is this", and a deploy record has to say `unknown`.
ARG GIT_REVISION=unknown
LABEL org.opencontainers.image.revision=$GIT_REVISION

CMD ["python3", "/app/server.py"]
