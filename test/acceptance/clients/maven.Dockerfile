# The Maven client for the acceptance features (decision 0015): Maven 3.10 on
# Temurin 21. Pinned by digest; Dependabot's docker entry moves it. The job
# builds it as artifactweave-accept-maven and runs it with --network host, so
# mvn reaches the binary on 127.0.0.1. maven-3.9/Dockerfile is the second
# Maven client (#284).
FROM maven:3.10.0-eclipse-temurin-21@sha256:d23342b1a070a07555f4ef20c085a81aa40e5f5b3288ebf8ddae2f67e9d760c8
