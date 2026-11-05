# Client pins: Puppet 8.10.0 on Ruby 3.3.13.
# Puppet 8.10.0, the newest Puppet gem, needs Facter 4, and every Facter 4
# release declares Ruby below 4.0, so the gem cannot install on Ruby 4;
# RubyGems 4 crashes while reporting the conflict. Dependabot moved this
# image to Ruby 4 three times (#895, #906, #934).
# It has a directory of its own because Dependabot's entry for it keeps it
# below Ruby 4, which an entry shared with the other Ruby images would do to
# them too. The job builds it as artifactweave-accept-puppet.
FROM ruby:3.3.13-slim@sha256:c6c9935e104f9fbc58b7a5d5a0d8aabe78a91660f9c0313b89e18abf7d0de355
RUN gem install --no-document puppet -v 8.10.0 && puppet --version
ENTRYPOINT ["puppet"]
