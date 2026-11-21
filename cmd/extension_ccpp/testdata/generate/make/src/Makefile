CFLAGS = -DFROM_MAKE=1 -I../include

app: main.o
	$(CC) -o $@ main.o

main.o: main.c
	$(CC) $(CFLAGS) -c main.c -o main.o
