#include "network_preflight.h"

#include <stdlib.h>
#include <string.h>

int main(int argc, char **argv) {
    if (argc != 6 || argv == NULL ||
        strcmp(argv[1], "-H") != 0 ||
        strcmp(argv[2], "127.0.0.1") != 0 ||
        strcmp(argv[3], "-F") != 0 ||
        strcmp(argv[4], "-P") != 0 ||
        argv[5] == NULL) {
        return EXIT_FAILURE;
    }

    int exit_code = EXIT_FAILURE;
    if (!amdl_network_preflight_if_requested(argv[5], &exit_code)) {
        return EXIT_FAILURE;
    }
    return exit_code;
}
