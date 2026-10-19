package org.acme.smoke;

/** The class maven.feature deploys as a snapshot. */
public final class Snap {
    private Snap() {}

    /** Greets the caller. */
    public static String greeting() {
        return "a snapshot from artifactweave";
    }
}
