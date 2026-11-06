# Client pins: Puppet 8.10.0 on Ruby 3.3.13.
# Puppet 8.10.0, the newest Puppet gem, installs on Ruby 3.4 but cannot
# extract a module archive there: Puppet::Util.safe_posix_fork closes every
# descriptor it lists in /proc/self/fd, including the one its own Dir.foreach
# is reading, and Ruby 3.4 raises Errno::EBADF on that read where Ruby 3.3
# ignored it. Dependabot's #1121 moved this image to Ruby 3.4 and every
# Puppet scenario failed (#1126; Red Hat bug 2349352). On Ruby 4 the gem
# does not install at all: it needs Facter 4, every Facter 4 release
# declares Ruby below 4.0, and RubyGems 4 crashes while reporting the
# conflict (#895, #906, #934).
# It has a directory of its own because Dependabot's entry for it keeps it
# below Ruby 3.4, which an entry shared with the other Ruby images would do to
# them too. The job builds it as artifactweave-accept-puppet.
FROM ruby:3.3.13-slim@sha256:8743a2d8ce3a33aed0de5c009dca0542e5fa85a4bd5b43170ce7b4af8c23809a
RUN gem install --no-document puppet -v 8.10.0 && puppet --version
ENTRYPOINT ["puppet"]
