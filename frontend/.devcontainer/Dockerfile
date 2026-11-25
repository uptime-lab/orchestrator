FROM node:26@sha256:e18c3ccdf7d71619990b0e85639f506e2028a95410999cab24cc373cbd10e0ac

EXPOSE 5174

RUN apt-get update && \
    apt-get install -y \
    curl \
    unzip \
    procps \
    libxext6 \
    libxrender1 \
    libxtst6 \
    libxi6 \
    libfreetype6

WORKDIR /workspace
RUN chown node:node /workspace && chmod 755 /workspace

COPY pnpm-lock.yaml ./
RUN npm install -g pnpm

USER node
COPY . .
RUN pnpm install

CMD ["pnpm", "run", "dev"]
