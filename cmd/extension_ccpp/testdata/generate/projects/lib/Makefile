CFLAGS = -DFROM_LIB=1

lib.a: lib.o
	$(AR) rcs lib.a lib.o

lib.o: lib.c
	$(CC) $(CFLAGS) -c lib.c -o lib.o
