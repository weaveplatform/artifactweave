# Client pins: Maven 3 image, Temurin 27, Ivy 2.6.0, Gradle 9.8.2.
# Maven 3 on Eclipse Temurin. Pinned by digest; Dependabot's docker entry moves
# it, JDK included, and is not held back for a JFrog client fault (#523). The job
# builds it as artifactweave-accept-maven and runs it with --network host, so
# mvn reaches the binary on 127.0.0.1. maven-3.9/Dockerfile is the second
# Maven client (#284).
FROM maven:3-eclipse-temurin-27@sha256:a6cd0d45e565ec76bf1969702bcbdb800d3461c8338012728e891b8681374575

# UX-01 verifies Ivy and direct Gradle recipes with released clients too.
# Checksums are the upstream Maven Central SHA-512 and Gradle release SHA-256.
# Each has a second source for the same bytes (#1322). Ivy falls back to
# Google's mirror of Maven Central, which maven.apache.org/repository names.
# services.gradle.org only redirects to the gradle/gradle-distributions GitHub
# release, so Gradle falls back to that release directly.
COPY fetch-pinned.sh /usr/local/bin/fetch-pinned
RUN fetch-pinned 0e66c0908287112b8a63e468a572ea265767225422bfb17ff3ec9deaa647d2aafbf79b0fa18d021fc3a3e0f8cabc7c07e7c4e613f1d49a538987c49df836dd29 /opt/ivy.jar \
      https://repo.maven.apache.org/maven2/org/apache/ivy/ivy/2.6.0/ivy-2.6.0.jar \
      https://maven-central.storage-download.googleapis.com/maven2/org/apache/ivy/ivy/2.6.0/ivy-2.6.0.jar \
    && fetch-pinned 21bd5da2de49b642904077f38c0efdc7778e92b3ed0889392dca61b97e389607 /tmp/gradle.zip \
      https://services.gradle.org/distributions/gradle-9.8.2-bin.zip \
      https://github.com/gradle/gradle-distributions/releases/download/v9.8.2/gradle-9.8.2-bin.zip \
    && mkdir -p /opt/gradle \
    && cd /opt/gradle && jar xf /tmp/gradle.zip \
    && rm /tmp/gradle.zip \
    && ln -s /opt/gradle/gradle-9.8.2/bin/gradle /usr/local/bin/gradle
# The JDK jar extractor does not preserve the ZIP's Unix executable bit.
RUN chmod 0755 /opt/gradle/gradle-9.8.2/bin/gradle \
    && gradle --version | grep -F 'Gradle 9.8.2'

# Retain the installed client identity in the native image build log.
RUN curl --version
