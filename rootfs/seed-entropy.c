/*
 * seed-entropy.c — Seed the kernel entropy pool in Firecracker MicroVMs.
 *
 * Firecracker VMs boot with very little entropy. This program gathers
 * data from multiple kernel sources, hashes it, and credits it to the
 * kernel random pool via RNDADDENTROPY ioctl.
 *
 * Replaces the Python entropy seeding script to eliminate the python3
 * dependency from the rootfs.
 *
 * Build: gcc -O2 -static -o seed-entropy seed-entropy.c
 */

#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/time.h>
#include <time.h>
#include <unistd.h>

/* linux/random.h defines */
#define RNDADDENTROPY 0x40085203

struct rand_pool_info {
    int entropy_count;
    int buf_size;
    unsigned char buf[];
};

#define POOL_SIZE 512
#define MAX_SOURCE 2048

/*
 * Simple hash: DJB2 variant iterated to spread bits.
 * Not cryptographic, but sufficient for entropy seeding —
 * the kernel will mix this further through its own CSPRNG.
 */
static void hash_into(unsigned char *pool, int pool_len,
                      const unsigned char *data, int data_len) {
    unsigned long h = 5381;
    for (int i = 0; i < data_len; i++) {
        h = ((h << 5) + h) ^ data[i];
    }
    for (int i = 0; i < pool_len; i++) {
        h = ((h << 5) + h) ^ (i + 1);
        pool[i] ^= (unsigned char)(h & 0xFF);
    }
}

static int read_file_into(unsigned char *pool, int pool_len,
                          const char *path) {
    unsigned char buf[MAX_SOURCE];
    int fd = open(path, O_RDONLY);
    if (fd < 0)
        return 0;
    int n = read(fd, buf, sizeof(buf));
    close(fd);
    if (n > 0)
        hash_into(pool, pool_len, buf, n);
    return n > 0 ? n : 0;
}

int main(void) {
    unsigned char pool[POOL_SIZE];
    memset(pool, 0, sizeof(pool));

    int sources = 0;

    /* Time-based entropy */
    struct timeval tv;
    gettimeofday(&tv, NULL);
    hash_into(pool, POOL_SIZE, (unsigned char *)&tv, sizeof(tv));
    sources++;

    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    hash_into(pool, POOL_SIZE, (unsigned char *)&ts, sizeof(ts));
    sources++;

    /* Process IDs */
    pid_t pid = getpid();
    pid_t ppid = getppid();
    hash_into(pool, POOL_SIZE, (unsigned char *)&pid, sizeof(pid));
    hash_into(pool, POOL_SIZE, (unsigned char *)&ppid, sizeof(ppid));
    sources++;

    /* Kernel sources */
    static const char *paths[] = {
        "/proc/uptime",
        "/proc/interrupts",
        "/proc/stat",
        "/proc/sys/kernel/random/boot_id",
        "/proc/self/stat",
        "/proc/self/maps",
        NULL
    };
    for (int i = 0; paths[i]; i++) {
        if (read_file_into(pool, POOL_SIZE, paths[i]) > 0)
            sources++;
    }

    /* Credit entropy to kernel pool */
    int fd = open("/dev/urandom", O_WRONLY);
    if (fd < 0) {
        fprintf(stderr, "seed-entropy: cannot open /dev/urandom: %s\n",
                strerror(errno));
        return 1;
    }

    struct rand_pool_info *info = malloc(sizeof(*info) + POOL_SIZE);
    if (!info) {
        close(fd);
        return 1;
    }
    info->entropy_count = 512; /* credit 512 bits */
    info->buf_size = POOL_SIZE;
    memcpy(info->buf, pool, POOL_SIZE);

    if (ioctl(fd, RNDADDENTROPY, info) < 0) {
        fprintf(stderr, "seed-entropy: RNDADDENTROPY failed: %s\n",
                strerror(errno));
        free(info);
        close(fd);
        return 1;
    }

    free(info);
    close(fd);

    printf("  [OK] seed-entropy: credited 512 bits from %d sources\n",
           sources);
    return 0;
}
