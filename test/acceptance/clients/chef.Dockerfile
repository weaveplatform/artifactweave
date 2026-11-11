# Client pins: Chef 19.4.46, Knife 19.0.134, knife-art 1.0.2 (Ruby 4.0.9).
FROM ruby:4.0.9-slim@sha256:d8cb2c0714bdaba0f318af897a01d2dbde3cfa8e62d47e99cea55feb13c0e90d
RUN apt-get update && apt-get install -y --no-install-recommends build-essential git libffi-dev libyaml-dev && rm -rf /var/lib/apt/lists/*
RUN gem install --no-document chef -v 19.4.46 && gem install --no-document knife -v 19.0.134 && gem install --no-document knife-art -v 1.0.2
ENV CHEF_LICENSE=accept-no-persist
ENTRYPOINT ["knife"]

# Retain the installed client identity in the native image build log.
RUN knife --version \
    && ruby -e '%w[chef knife knife-art].each { |name| puts name + " " + Gem::Specification.find_by_name(name).version.to_s }'
