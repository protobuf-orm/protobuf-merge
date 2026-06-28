FROM ghcr.io/lesomnus/dev-golang:1.26

RUN VERSION="1.55.1"; \
	curl -fsSL "https://github.com/bufbuild/buf/releases/download/v${VERSION}/buf-$(uname -s)-$(uname -m).tar.gz" \
		| tar -xvzf - -C /usr/local --strip-components 1 buf/etc buf/bin buf/share/zsh
