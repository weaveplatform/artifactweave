# Client pins: RubyGems 4.0.23 and Bundler 4.0.23 on Ruby 4.0.9.
FROM ruby:4.0.9-slim@sha256:d8cb2c0714bdaba0f318af897a01d2dbde3cfa8e62d47e99cea55feb13c0e90d
RUN cd /tmp \
 && gem fetch rubygems-update -v 4.0.23 \
 && gem fetch bundler -v 4.0.23 \
 && echo 'f872e6386caaafdd9fc5ade037ac3bd5c9203cd63723a8d31790b15a0a669629  rubygems-update-4.0.23.gem' | sha256sum -c - \
 && echo '407ba8a1d928f412847f819aa5f8b6aef7c59fb51feb517112af8a165487287b  bundler-4.0.23.gem' | sha256sum -c - \
 && gem install --local --no-document rubygems-update-4.0.23.gem \
 && update_rubygems --no-document \
 && gem install --local --no-document bundler-4.0.23.gem \
 && rm rubygems-update-4.0.23.gem bundler-4.0.23.gem \
 && ruby --version && gem --version && bundle --version
