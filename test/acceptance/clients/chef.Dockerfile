# Current public Knife/Chef and JFrog's authentication plugin, checked 2026-10-28.
FROM ruby:4.0.9-slim@sha256:88f0d8f1f2f7829c8c85c5cfee774f7ac936a7ba460e904e8c873b6a7f96c311
RUN apt-get update && apt-get install -y --no-install-recommends build-essential git libffi-dev libyaml-dev && rm -rf /var/lib/apt/lists/*
RUN gem install --no-document chef -v 19.4.46 && gem install --no-document knife -v 19.0.134 && gem install --no-document knife-art -v 1.0.2
ENV CHEF_LICENSE=accept-no-persist
ENTRYPOINT ["knife"]
