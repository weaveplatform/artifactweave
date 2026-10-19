package org.acme.smoke;

/** Compiles only when both the release and the snapshot dependency resolved. */
public final class App {
    private App() {}

    /** Greets the caller with both dependencies' greetings. */
    public static String greeting() {
        return Lib.greeting() + ", " + Snap.greeting();
    }
}
