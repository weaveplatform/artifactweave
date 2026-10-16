# The Maven client for the acceptance features (decision 0015): Maven 3.9 on
# Temurin 21. Pinned by digest; Dependabot's docker entry moves it. The job
# builds it as artifactweave-accept-maven and runs it with --network host, so
# mvn reaches the binary on 127.0.0.1.
FROM maven:3.9.16-eclipse-temurin-21@sha256:99e61abcff91a9b1333463bd8451fb18495d6eba9250ac66a338b518f8278320
