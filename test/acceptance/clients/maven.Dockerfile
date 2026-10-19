# The Maven client for the acceptance features (decision 0015): the newest
# Maven 3 on Eclipse Temurin. Pinned by digest; Dependabot's docker entry moves
# it, JDK included, and is not held back for a JFrog client fault (#523). The job
# builds it as artifactweave-accept-maven and runs it with --network host, so
# mvn reaches the binary on 127.0.0.1. maven-3.9/Dockerfile is the second
# Maven client (#284).
FROM maven:3-eclipse-temurin-26@sha256:69a6c37f0a91fca2c9d0cdd684c9c0ea7b1bc00d4196749c24440b2e02ad163d
