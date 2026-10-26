# Official R 4.5.2, pinned to the multi-platform manifest index.
FROM r-base:4.5.2@sha256:c84d2d97cb9c4d895b219d83a5fd480302a6ad0d2fd449f3cda7978583909a02

RUN Rscript -e 'stopifnot(as.character(getRversion()) == "4.5.2")'
