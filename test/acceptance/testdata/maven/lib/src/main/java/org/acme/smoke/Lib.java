package org.acme.smoke;

/** The class the acceptance features compile, deploy and resolve. */
public final class Lib {
    private Lib() {}

    /** Greets the caller. */
    public static String greeting() {
        return "hello from artifactweave";
    }
}
